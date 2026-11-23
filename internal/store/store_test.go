package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRequestKeepsItsDocument(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	r := &Request{Person: "a@example.com", Repo: "acme/api", App: "acme-web", Doc: Doc{Text: "the page is blank", Lane: "user",
		Context: &Context{URL: "https://app.example.com/orders"}, HandleHash: "abc", Feedback: "fix it", AnnalsKey: "k1"}}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.State != StateReceived || r.ID == "" {
		t.Fatalf("created: %+v", r)
	}
	r.State, r.Verdict = StatePlanning, &Verdict{Verdict: VerdictDefect, Facts: []Fact{{Text: "nil map", Evidence: []int{1}}}}
	if err := s.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StatePlanning || got.Verdict.Facts[0].Evidence[0] != 1 || got.Feedback != "fix it" || got.HandleHash != "abc" || got.AnnalsKey != "k1" || got.Context.URL == "" {
		t.Fatalf("read back: %+v", got)
	}
	if by, err := s.ByHandle(ctx, "abc"); err != nil || by.ID != r.ID {
		t.Fatalf("by handle: %v %v", by, err)
	}
	if _, err := s.ByHandle(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSaveIfLetsTheFirstMoveWin(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	r := &Request{Person: "a@example.com", Repo: "acme/api", App: "graft"}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	other, _ := s.Get(ctx, r.ID)
	r.State = StateScreening
	if err := s.SaveIf(ctx, r, StateReceived); err != nil {
		t.Fatal(err)
	}
	other.State = StateDeclined
	if err := s.SaveIf(ctx, other, StateReceived); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestListFilters(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	mk := func(person, repo string) *Request {
		r := &Request{Person: person, Repo: repo, App: "graft"}
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	mk("a@example.com", "acme/api")
	mk("b@example.com", "acme/api")
	done := mk("b@example.com", "acme/web")
	done.State = StateAnswered
	if err := s.Save(ctx, done); err != nil {
		t.Fatal(err)
	}
	count := func(f Filter) int {
		got, err := s.List(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if n := count(Filter{}); n != 3 {
		t.Errorf("all = %d", n)
	}
	if n := count(Filter{Person: "a@example.com"}); n != 1 {
		t.Errorf("a's = %d", n)
	}
	if n := count(Filter{Person: "a@example.com", Repos: []string{"acme/web"}}); n != 2 {
		t.Errorf("a's and web's = %d", n)
	}
	if n := count(Filter{States: WorkStates}); n != 2 {
		t.Errorf("working = %d", n)
	}
	if n, err := s.CountSince(ctx, "b@example.com", time.Now().Add(-time.Hour)); err != nil || n != 2 {
		t.Errorf("b's in the hour = %d %v", n, err)
	}
}

func TestEvidenceIsNumberedPerRequest(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a := &Request{Person: "a@example.com", Repo: "acme/api", App: "graft"}
	b := &Request{Person: "a@example.com", Repo: "acme/api", App: "graft"}
	for _, r := range []*Request{a, b} {
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []int{1, 2} {
		n, err := s.AddEvidence(ctx, a.ID, "tool", "queue-depth", "7")
		if err != nil || n != want {
			t.Fatalf("evidence %d: n=%d %v", i, n, err)
		}
	}
	if n, err := s.AddEvidence(ctx, b.ID, "code", "main.go:10", "the nil map"); err != nil || n != 1 {
		t.Fatalf("another request starts at 1: %d %v", n, err)
	}
	ev, err := s.Evidence(ctx, a.ID)
	if err != nil || len(ev) != 2 || ev[1].N != 2 {
		t.Fatalf("evidence: %+v %v", ev, err)
	}
}

func TestContextExpires(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	r := &Request{Person: "a@example.com", Repo: "acme/api", App: "acme-web", Doc: Doc{Text: "blank", Context: &Context{URL: "https://app.example.com"}}}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireContext(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("too young to drop: %d %v", n, err)
	}
	if n, err := s.ExpireContext(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("dropped %d %v", n, err)
	}
	got, _ := s.Get(ctx, r.ID)
	if got.Context != nil || got.Text != "blank" {
		t.Fatalf("after expiry: %+v", got.Doc)
	}
}

func TestSwitchReleasesAndTools(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if sw, err := s.Switch(ctx, "acme/api"); err != nil || sw.Stopped {
		t.Fatalf("a repository starts on: %+v %v", sw, err)
	}
	if err := s.SetSwitch(ctx, Switch{Repo: "acme/api", Stopped: true, Reason: "incident:42", By: "graft"}); err != nil {
		t.Fatal(err)
	}
	if sw, _ := s.Switch(ctx, "acme/api"); !sw.Stopped || sw.Reason != "incident:42" {
		t.Fatalf("stopped: %+v", sw)
	}
	if at, _, err := s.LastRelease(ctx, "acme/api"); err != nil || !at.IsZero() {
		t.Fatalf("no release yet: %v %v", at, err)
	}
	if err := s.AddRelease(ctx, "acme/api", "r1", "abc"); err != nil {
		t.Fatal(err)
	}
	if at, req, _ := s.LastRelease(ctx, "acme/api"); at.IsZero() || req != "r1" {
		t.Fatalf("last release: %v %s", at, req)
	}
	if err := s.SetDeclaredTools(ctx, "acme/api", []string{"b", "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeclaredTools(ctx, "acme/api", []string{"c"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.DeclaredTools(ctx, "acme/api"); len(got) != 1 || got[0] != "c" {
		t.Fatalf("declared: %v", got)
	}
}

func TestAPlanKeptBeforeBriefsBecomesItsBuildPlanAndItsNoteItsThread(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// A request as the store before kept it.
	old := `{"lane":"user","text":"add a line","planRound":1,"decidedBy":"lead@example.com","decidedAt":"2026-11-18T15:00:00Z","planNote":"Keep the old line.",` +
		`"ticket":{"number":7,"url":"https://gitlab.example.com/acme/api/-/issues/7"},` +
		`"plan":{"understood":"needs a line","changes":[{"path":"a.txt","what":"add it"}],"untouched":[],"tests":"two.sh","size":{"lines":2,"files":1},"tiers":[],"kind":"feature"}}`
	if _, err := s.db.Exec(`INSERT INTO requests (id, created_at, updated_at, state, person, repo, app, doc) VALUES ('r1', ?, ?, 'investigating', 'a@example.com', 'acme/api', 'graft', ?)`, now(), now(), old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.Get(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	b := r.Plan.Build
	if b.Kind != KindFeature || b.Understood != "needs a line" || b.Changes[0].Path != "a.txt" || b.Size.Lines != 2 || r.Plan.Brief.Asked != "" {
		t.Errorf("plan: %+v", r.Plan)
	}
	if len(r.Thread) != 1 || !reflect.DeepEqual(r.Thread[0], Note{Round: 1, From: FromHub, By: "lead@example.com", At: "2026-11-18T15:00:00Z", Text: "Keep the old line."}) {
		t.Errorf("thread: %+v", r.Thread)
	}
	raw := ""
	_ = s.db.QueryRow(`SELECT doc FROM requests WHERE id = 'r1'`).Scan(&raw)
	if strings.Contains(raw, "planNote") {
		t.Errorf("the note is still kept apart: %s", raw)
	}
	if got, err := s.ByTicket(ctx, "acme/api", 7); err != nil || got.ID != "r1" {
		t.Errorf("by ticket: %v %v", got, err)
	}
	if _, err := s.ByTicket(ctx, "acme/api", 8); !errors.Is(err, ErrNotFound) {
		t.Errorf("an issue that is no ticket: %v", err)
	}
}
