// Package store keeps requests in a SQLite file: each request, its numbered
// evidence, its events, and the few things Graft remembers per repository.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

var ErrNotFound = errors.New("not found")

// ErrConflict is a conditional save of a request no longer in the state it
// was read in.
var ErrConflict = errors.New("the request moved on")

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrations run in order, once each; the database's user_version counts
// how many have run. A shipped migration is never edited; add the next one.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  idea TEXT NOT NULL,
  state TEXT NOT NULL,
  step TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  slug TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  stack TEXT NOT NULL DEFAULT '',
  repo_url TEXT NOT NULL DEFAULT '',
  app_url TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  rounds INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  at TEXT NOT NULL,
  step TEXT NOT NULL,
  message TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_job ON events(job_id, id);`,
	`ALTER TABLE jobs RENAME COLUMN idea TO brief;
ALTER TABLE jobs ADD COLUMN mode TEXT NOT NULL DEFAULT 'new';
ALTER TABLE jobs ADD COLUMN repo TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN base TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN feedback TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE jobs ADD COLUMN protected TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN pr_url TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN summary TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN review_head TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN approved_head TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN asked_at TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE jobs ADD COLUMN orrery_run TEXT NOT NULL DEFAULT '';`,
	`DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS jobs;
CREATE TABLE requests (
  id TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  state TEXT NOT NULL,
  person TEXT NOT NULL,
  repo TEXT NOT NULL,
  app TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX requests_person ON requests(person, created_at);
CREATE INDEX requests_repo ON requests(repo, created_at);
CREATE INDEX requests_state ON requests(state);
CREATE TABLE evidence (
  request TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  n INTEGER NOT NULL,
  kind TEXT NOT NULL,
  title TEXT NOT NULL,
  body TEXT NOT NULL,
  at TEXT NOT NULL,
  PRIMARY KEY (request, n)
);
CREATE TABLE request_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  at TEXT NOT NULL,
  stage TEXT NOT NULL,
  message TEXT NOT NULL
);
CREATE INDEX request_events_request ON request_events(request, id);
CREATE TABLE switches (
  repo TEXT PRIMARY KEY,
  stopped INTEGER NOT NULL,
  reason TEXT NOT NULL,
  by TEXT NOT NULL,
  at TEXT NOT NULL
);
CREATE TABLE releases (
  repo TEXT NOT NULL,
  request TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  at TEXT NOT NULL
);
CREATE INDEX releases_repo ON releases(repo, at);
CREATE TABLE tools (
  repo TEXT NOT NULL,
  action TEXT NOT NULL,
  PRIMARY KEY (repo, action)
);`,
	// A plan is a brief and a build plan: a plan kept before is its build
	// plan. A note a plan was sent back with is the first of its thread.
	`UPDATE requests SET doc = json_set(doc, '$.plan', json_object('brief', json_object(), 'build', json(json_extract(doc, '$.plan'))))
  WHERE json_type(doc, '$.plan') = 'object' AND json_type(doc, '$.plan.build') IS NULL;
UPDATE requests SET doc = json_remove(json_set(doc, '$.thread', json_array(json_object(
    'round', coalesce(json_extract(doc, '$.planRound'), 0), 'from', 'hub',
    'by', coalesce(json_extract(doc, '$.decidedBy'), ''), 'at', coalesce(json_extract(doc, '$.decidedAt'), ''),
    'text', json_extract(doc, '$.planNote')))), '$.planNote')
  WHERE coalesce(json_extract(doc, '$.planNote'), '') != '';
UPDATE requests SET doc = json_remove(doc, '$.planNote') WHERE json_type(doc, '$.planNote') IS NOT NULL;`,
	// Each run of a stage is one record, and one comment on the ticket.
	`CREATE TABLE IF NOT EXISTS runs (
  request TEXT NOT NULL,
  n INTEGER NOT NULL,
  doc TEXT NOT NULL,
  PRIMARY KEY (request, n)
);`,
	// Each prompt an agent runtime ran, with what the runtime said it spent.
	`CREATE TABLE IF NOT EXISTS agent_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request TEXT NOT NULL,
  at TEXT NOT NULL,
  doc TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_runs_request ON agent_runs(request, id);`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// NewID is a new request's id. A request made with one already keeps it.
func NewID() string { return newID() }

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

const stamp = time.RFC3339Nano

func now() string { return time.Now().UTC().Format(stamp) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(stamp, s)
	return t
}

func (r *Request) doc() (string, error) {
	b, err := json.Marshal(stored{Doc: r.Doc, Feedback: r.Feedback, HandleHash: r.HandleHash, AnnalsKey: r.AnnalsKey, Seq: r.Seq})
	return string(b), err
}

// Create keeps a new request, in the received state. A request with no id
// gets a new one.
func (s *Store) Create(ctx context.Context, r *Request) error {
	if r.ID == "" {
		r.ID = newID()
	}
	r.State = StateReceived
	r.CreatedAt = time.Now().UTC()
	r.UpdatedAt = r.CreatedAt
	doc, err := r.doc()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO requests (id, created_at, updated_at, state, person, repo, app, doc) VALUES (?,?,?,?,?,?,?,?)`,
		r.ID, r.CreatedAt.Format(stamp), r.UpdatedAt.Format(stamp), r.State, r.Person, r.Repo, r.App, doc)
	return err
}

func (s *Store) Save(ctx context.Context, r *Request) error { return s.save(ctx, r, "") }

// SaveIf saves the request only while the stored one is still in state
// from, and answers ErrConflict otherwise: of a worker and a person moving
// the same request at once, the first one wins.
func (s *Store) SaveIf(ctx context.Context, r *Request, from string) error {
	return s.save(ctx, r, from)
}

func (s *Store) save(ctx context.Context, r *Request, from string) error {
	doc, err := r.doc()
	if err != nil {
		return err
	}
	r.UpdatedAt = time.Now().UTC()
	query, args := `UPDATE requests SET state=?, updated_at=?, doc=? WHERE id=?`, []any{r.State, r.UpdatedAt.Format(stamp), doc, r.ID}
	if from != "" {
		query += ` AND state=?`
		args = append(args, from)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if from != "" {
			if _, err := s.Get(ctx, r.ID); err == nil {
				return ErrConflict
			}
		}
		return ErrNotFound
	}
	return nil
}

const cols = `id, created_at, updated_at, state, person, repo, app, doc`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (*Request, error) {
	var r Request
	var created, updated, doc string
	if err := row.Scan(&r.ID, &created, &updated, &r.State, &r.Person, &r.Repo, &r.App, &doc); err != nil {
		return nil, err
	}
	var st stored
	if err := json.Unmarshal([]byte(doc), &st); err != nil {
		return nil, fmt.Errorf("request %s: %w", r.ID, err)
	}
	r.Doc = st.Doc
	r.Feedback, r.HandleHash, r.AnnalsKey, r.Seq = st.Feedback, st.HandleHash, st.AnnalsKey, st.Seq
	r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
	return &r, nil
}

func (s *Store) Get(ctx context.Context, id string) (*Request, error) {
	r, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM requests WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// Filter picks requests. Empty fields pick everything.
type Filter struct {
	Person string
	Repos  []string
	States []string
	// Since keeps requests made at or after it.
	Since time.Time
	Limit int
}

// List returns the requests a filter picks, newest first. With both a
// person and repositories, it returns that person's and those repositories'.
func (s *Store) List(ctx context.Context, f Filter) ([]*Request, error) {
	var where []string
	var args []any
	marks := func(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }
	var who []string
	if f.Person != "" {
		who = append(who, `person = ?`)
		args = append(args, f.Person)
	}
	if len(f.Repos) > 0 {
		who = append(who, `repo IN (`+marks(len(f.Repos))+`)`)
		for _, r := range f.Repos {
			args = append(args, r)
		}
	}
	if len(who) > 0 {
		where = append(where, `(`+strings.Join(who, ` OR `)+`)`)
	}
	if len(f.States) > 0 {
		where = append(where, `state IN (`+marks(len(f.States))+`)`)
		for _, st := range f.States {
			args = append(args, st)
		}
	}
	if !f.Since.IsZero() {
		where = append(where, `created_at >= ?`)
		args = append(args, f.Since.UTC().Format(stamp))
	}
	q := `SELECT ` + cols + ` FROM requests`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, ` AND `)
	}
	q += ` ORDER BY created_at DESC`
	if f.Limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Request
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountSince counts the requests a person made at or after t.
func (s *Store) CountSince(ctx context.Context, person string, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM requests WHERE person=? AND created_at>=?`, person, t.UTC().Format(stamp)).Scan(&n)
	return n, err
}

// ByHandle returns the request whose handle secret hashes to hash.
func (s *Store) ByHandle(ctx context.Context, hash string) (*Request, error) {
	if hash == "" {
		return nil, ErrNotFound
	}
	r, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM requests WHERE json_extract(doc, '$.handleHash') = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ByTicket returns the request whose ticket is issue number of repo.
func (s *Store) ByTicket(ctx context.Context, repo string, number int) (*Request, error) {
	r, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM requests WHERE repo = ? AND json_extract(doc, '$.ticket.number') = ? ORDER BY created_at DESC LIMIT 1`, repo, number))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// MarkReadProduction records that a tool read production for the request.
// It changes that one field alone, so a worker holding its own copy of the
// request loses nothing.
func (s *Store) MarkReadProduction(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requests SET doc = json_set(doc, '$.readProduction', json('true')) WHERE id = ?`, id)
	return err
}

// ExpireContext drops the captured context of every request made before t,
// and says how many it dropped.
func (s *Store) ExpireContext(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE requests SET doc = json_remove(doc, '$.context') WHERE created_at < ? AND json_extract(doc, '$.context') IS NOT NULL`, t.UTC().Format(stamp))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Attached lists the requests made before t that still name files.
func (s *Store) Attached(ctx context.Context, t time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM requests WHERE created_at < ? AND (json_extract(doc, '$.attachments') IS NOT NULL OR json_extract(doc, '$.incoming') IS NOT NULL)`, t.UTC().Format(stamp))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ForgetAttachments takes the files off a request, once they are gone from
// the disk. It changes that one field alone.
func (s *Store) ForgetAttachments(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requests SET doc = json_remove(doc, '$.attachments', '$.incoming') WHERE id = ?`, id)
	return err
}

// AddEvidence keeps one thing an investigation read and returns its number.
func (s *Store) AddEvidence(ctx context.Context, request, kind, title, body string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `INSERT INTO evidence (request, n, kind, title, body, at)
		VALUES (?, (SELECT coalesce(max(n), 0) + 1 FROM evidence WHERE request = ?), ?, ?, ?, ?) RETURNING n`,
		request, request, kind, title, body, now()).Scan(&n)
	return n, err
}

func (s *Store) Evidence(ctx context.Context, request string) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n, kind, title, body, at FROM evidence WHERE request=? ORDER BY n`, request)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		var e Evidence
		var at string
		if err := rows.Scan(&e.N, &e.Kind, &e.Title, &e.Body, &at); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) AddEvent(ctx context.Context, request, stage, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO request_events (request, at, stage, message) VALUES (?,?,?,?)`, request, now(), stage, message)
	return err
}

func (s *Store) Events(ctx context.Context, request string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT at, stage, message FROM request_events WHERE request=? ORDER BY id`, request)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at string
		if err := rows.Scan(&at, &e.Stage, &e.Message); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Switch is the repository's stop switch; a repository never stopped is on.
func (s *Store) Switch(ctx context.Context, repo string) (Switch, error) {
	sw := Switch{Repo: repo}
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT stopped, reason, by, at FROM switches WHERE repo=?`, repo).Scan(&sw.Stopped, &sw.Reason, &sw.By, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return sw, nil
	}
	sw.At = parseTime(at)
	return sw, err
}

// SetSwitch stops or starts a repository.
func (s *Store) SetSwitch(ctx context.Context, sw Switch) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO switches (repo, stopped, reason, by, at) VALUES (?,?,?,?,?)
		ON CONFLICT (repo) DO UPDATE SET stopped=excluded.stopped, reason=excluded.reason, by=excluded.by, at=excluded.at`,
		sw.Repo, sw.Stopped, sw.Reason, sw.By, now())
	return err
}

// AddRelease records that a request's change shipped to a repository.
func (s *Store) AddRelease(ctx context.Context, repo, request, sha string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO releases (repo, request, commit_sha, at) VALUES (?,?,?,?)`, repo, request, sha, now())
	return err
}

// LastRelease is when Graft last shipped to a repository, and for which
// request; the zero time when it never has.
func (s *Store) LastRelease(ctx context.Context, repo string) (time.Time, string, error) {
	var at, request string
	err := s.db.QueryRowContext(ctx, `SELECT at, request FROM releases WHERE repo=? ORDER BY at DESC LIMIT 1`, repo).Scan(&at, &request)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", nil
	}
	return parseTime(at), request, err
}

// DeclaredTools are the hub actions Graft declared for a repository.
func (s *Store) DeclaredTools(ctx context.Context, repo string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT action FROM tools WHERE repo=? ORDER BY action`, repo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetDeclaredTools replaces the hub actions kept for a repository.
func (s *Store) SetDeclaredTools(ctx context.Context, repo string, actions []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM tools WHERE repo=?`, repo); err != nil {
		return err
	}
	for _, a := range actions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tools (repo, action) VALUES (?,?)`, repo, a); err != nil {
			return err
		}
	}
	return tx.Commit()
}
