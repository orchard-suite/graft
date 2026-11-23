// Package gitlab is the GitLab adapter of the forge crossing, over GitLab's
// REST API, version 4. A pull request there is a merge request.
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orchard-suite/graft/internal/forge"
)

// Forge speaks to one GitLab.
type Forge struct {
	web    string
	rest   *forge.REST
	tokens forge.Tokens

	mu sync.Mutex
	// users are the forge users the tokens belong to, by token.
	users map[string]int64
}

// New returns the adapter for the GitLab at web. tokens gives the token
// for a repository: a project or group access token.
func New(web string, tokens forge.Tokens) *Forge {
	web = strings.TrimRight(web, "/")
	return &Forge{web: web, tokens: tokens, users: map[string]int64{}, rest: &forge.REST{
		Base: web + "/api/v4",
		HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: forge.KeepOnHost("PRIVATE-TOKEN")},
		Auth: func(req *http.Request, token string) { req.Header.Set("PRIVATE-TOKEN", token) },
	}}
}

func (f *Forge) Kind() string                { return "gitlab" }
func (f *Forge) CloneURL(repo string) string { return f.web + "/" + repo + ".git" }
func (f *Forge) CommitURL(repo, sha string) string {
	return f.web + "/" + repo + "/-/commit/" + sha
}

func (f *Forge) Credential(ctx context.Context, in io.Reader, out io.Writer) error {
	return forge.Credential(ctx, f.web, "oauth2", f.tokens, in, out)
}

// do calls the API of one project, which GitLab names by its escaped path.
func (f *Forge) do(ctx context.Context, repo, method, path string, in, out any) error {
	raw, err := f.raw(ctx, repo, method, path, in)
	if err != nil || out == nil {
		return err
	}
	return decode(raw, out, method, path)
}

func (f *Forge) raw(ctx context.Context, repo, method, path string, in any) ([]byte, error) {
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return nil, err
	}
	return f.rest.Raw(ctx, tok, method, "/projects/"+url.PathEscape(repo)+path, in)
}

func (f *Forge) CreateIssue(ctx context.Context, repo, title, body string) (forge.Issue, error) {
	var res struct {
		IID int    `json:"iid"`
		URL string `json:"web_url"`
	}
	err := f.do(ctx, repo, http.MethodPost, "/issues", map[string]string{"title": title, "description": body}, &res)
	return forge.Issue{Number: res.IID, URL: res.URL}, err
}

func (f *Forge) UpdateIssue(ctx context.Context, repo string, number int, body string) error {
	return f.do(ctx, repo, http.MethodPut, fmt.Sprintf("/issues/%d", number), map[string]string{"description": body}, nil)
}

// Comments reads an issue's notes a page at a time, oldest first, and keeps
// those after after. A system note, which GitLab writes itself, is left out.
// A note by the user the token belongs to is Graft's own.
func (f *Forge) Comments(ctx context.Context, repo string, number int, after int64) ([]forge.Comment, error) {
	me, err := f.me(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Comment
	for page := 1; page <= maxPages; page++ {
		var notes []struct {
			ID      int64  `json:"id"`
			Body    string `json:"body"`
			System  bool   `json:"system"`
			Created string `json:"created_at"`
			Author  struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
				Bot      bool   `json:"bot"`
			} `json:"author"`
		}
		path := fmt.Sprintf("/issues/%d/notes?sort=asc&order_by=created_at&per_page=100&page=%d", number, page)
		if err := f.do(ctx, repo, http.MethodGet, path, nil, &notes); err != nil {
			return nil, err
		}
		for _, n := range notes {
			if n.ID <= after || n.System {
				continue
			}
			out = append(out, forge.Comment{ID: n.ID, Body: n.Body, At: n.Created, URL: fmt.Sprintf("%s/%s/-/issues/%d#note_%d", f.web, repo, number, n.ID),
				Author:  forge.User{ID: n.Author.ID, Username: n.Author.Username, Bot: n.Author.Bot, Mine: n.Author.ID == me},
				Uploads: f.uploads(repo, n.Body)})
		}
		if len(notes) < 100 {
			break
		}
	}
	return out, nil
}

// maxPages bounds how many pages of an issue's notes are read at once.
const maxPages = 20

// secretShape is the secret GitLab names an uploaded file by.
var secretShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

// uploads are the files a note links to that GitLab keeps for the
// project: a link GitLab writes when a file is attached, /uploads/<secret>/
// <name>, relative or under the project's own address on this GitLab. A
// link to anywhere else names none.
func (f *Forge) uploads(repo, body string) []forge.Upload {
	link := regexp.MustCompile(`(?:^|[\s(\[<"'])(?:` + regexp.QuoteMeta(f.web+"/"+repo) + `)?/uploads/([0-9a-f]{32})/([^\s/()\[\]<>"'?#]+)`)
	seen := map[string]bool{}
	var out []forge.Upload
	for _, m := range link.FindAllStringSubmatch(body, -1) {
		name, err := url.PathUnescape(m[2])
		if err != nil || !fileName(name) {
			continue
		}
		ref := m[1] + "/" + name
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, forge.Upload{Ref: ref, Name: name})
	}
	return out
}

// fileName says name is one file's name, with no folder in it.
func fileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// Upload reads a file GitLab keeps for the project, by its secret and
// name, through the project's uploads API (GitLab 17.4 and later).
func (f *Forge) Upload(ctx context.Context, repo, ref string, max int64) ([]byte, error) {
	secret, name, ok := strings.Cut(ref, "/")
	if !ok || !secretShape.MatchString(secret) || !fileName(name) {
		return nil, fmt.Errorf("gitlab: %q names no uploaded file", ref)
	}
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return nil, err
	}
	return f.rest.Fetch(ctx, tok, "/projects/"+url.PathEscape(repo)+"/uploads/"+secret+"/"+url.PathEscape(name), max)
}

// me is the id of the user the repository's token belongs to: a project or
// group access token is a bot user of its own. It is asked once a token.
func (f *Forge) me(ctx context.Context, repo string) (int64, error) {
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	id, ok := f.users[tok]
	f.mu.Unlock()
	if ok {
		return id, nil
	}
	var u struct {
		ID int64 `json:"id"`
	}
	if err := f.rest.Do(ctx, tok, http.MethodGet, "/user", nil, &u); err != nil {
		return 0, fmt.Errorf("who the forge token is: %w", err)
	}
	f.mu.Lock()
	f.users[tok] = u.ID
	f.mu.Unlock()
	return u.ID, nil
}

// Email is the public address a GitLab user shows on their profile, which
// GitLab lets a user set only to an address they have confirmed.
func (f *Forge) Email(ctx context.Context, repo string, u forge.User) (string, error) {
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return "", err
	}
	var user struct {
		PublicEmail string `json:"public_email"`
	}
	if err := f.rest.Do(ctx, tok, http.MethodGet, fmt.Sprintf("/users/%d", u.ID), nil, &user); err != nil {
		return "", err
	}
	return strings.TrimSpace(user.PublicEmail), nil
}

func (f *Forge) CommentIssue(ctx context.Context, repo string, number int, body string) (forge.Posted, error) {
	var note struct {
		ID int64 `json:"id"`
	}
	if err := f.do(ctx, repo, http.MethodPost, fmt.Sprintf("/issues/%d/notes", number), map[string]string{"body": body}, &note); err != nil {
		return forge.Posted{}, err
	}
	return forge.Posted{ID: note.ID, URL: fmt.Sprintf("%s/%s/-/issues/%d#note_%d", f.web, repo, number, note.ID)}, nil
}

// EditComment writes a note again. GitLab notifies nobody of an edit.
func (f *Forge) EditComment(ctx context.Context, repo string, number int, id int64, body string) error {
	return f.do(ctx, repo, http.MethodPut, fmt.Sprintf("/issues/%d/notes/%d", number, id), map[string]string{"body": body}, nil)
}

// Attach uploads a file to the project (POST /projects/:id/uploads) and
// answers the address GitLab names it by, /uploads/<secret>/<name>, which
// a note of the project links to as the project's own.
func (f *Forge) Attach(ctx context.Context, repo, name, contentType string, data []byte) (string, error) {
	if !fileName(name) {
		return "", fmt.Errorf("gitlab: %q is not a file name", name)
	}
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return "", err
	}
	var up struct {
		URL string `json:"url"`
	}
	if err := f.rest.Send(ctx, tok, "/projects/"+url.PathEscape(repo)+"/uploads", "file", name, contentType, data, &up); err != nil {
		return "", err
	}
	if !strings.HasPrefix(up.URL, "/uploads/") {
		return "", fmt.Errorf("gitlab: the upload came back as %q, not a project upload", up.URL)
	}
	return up.URL, nil
}

func (f *Forge) CloseIssue(ctx context.Context, repo string, number int) error {
	return f.do(ctx, repo, http.MethodPut, fmt.Sprintf("/issues/%d", number), map[string]string{"state_event": "close"}, nil)
}

func (f *Forge) OpenPR(ctx context.Context, repo, head, base, title, body string) (forge.PR, error) {
	var res struct {
		IID int    `json:"iid"`
		URL string `json:"web_url"`
	}
	err := f.do(ctx, repo, http.MethodPost, "/merge_requests", map[string]any{
		"source_branch": head, "target_branch": base, "title": title, "description": body, "remove_source_branch": true,
	}, &res)
	return forge.PR{Number: res.IID, URL: res.URL}, err
}

func (f *Forge) PR(ctx context.Context, repo string, number int) (forge.PRState, error) {
	var mr struct {
		State     string `json:"state"`
		SHA       string `json:"sha"`
		MergeSHA  string `json:"merge_commit_sha"`
		SquashSHA string `json:"squash_commit_sha"`
	}
	if err := f.do(ctx, repo, http.MethodGet, fmt.Sprintf("/merge_requests/%d", number), nil, &mr); err != nil {
		return forge.PRState{}, err
	}
	st := forge.PRState{State: forge.PROpen, Head: mr.SHA}
	switch mr.State {
	case "merged":
		st.State = forge.PRMerged
		// A fast-forward merge writes no commit of its own: main got the head.
		st.MergeSHA = mr.MergeSHA
		if st.MergeSHA == "" {
			st.MergeSHA = mr.SquashSHA
		}
		if st.MergeSHA == "" {
			st.MergeSHA = mr.SHA
		}
		return st, nil
	case "closed":
		st.State = forge.PRClosed
		return st, nil
	}
	var ap struct {
		Approved   bool       `json:"approved"`
		ApprovedBy []struct{} `json:"approved_by"`
	}
	if err := f.do(ctx, repo, http.MethodGet, fmt.Sprintf("/merge_requests/%d/approvals", number), nil, &ap); err != nil {
		return forge.PRState{}, err
	}
	// A project that asks for no approval calls every merge request
	// approved; a person must have said so.
	st.Approved = ap.Approved && len(ap.ApprovedBy) > 0
	return st, nil
}

func (f *Forge) MergePR(ctx context.Context, repo string, number int, sha string) error {
	return f.do(ctx, repo, http.MethodPut, fmt.Sprintf("/merge_requests/%d/merge", number), map[string]any{"sha": sha, "should_remove_source_branch": true}, nil)
}

func (f *Forge) ClosePR(ctx context.Context, repo string, number int, comment string) error {
	if comment != "" {
		if err := f.do(ctx, repo, http.MethodPost, fmt.Sprintf("/merge_requests/%d/notes", number), map[string]string{"body": comment}, nil); err != nil {
			return err
		}
	}
	return f.do(ctx, repo, http.MethodPut, fmt.Sprintf("/merge_requests/%d", number), map[string]string{"state_event": "close"}, nil)
}

func (f *Forge) DeleteBranch(ctx context.Context, repo, branch string) error {
	err := f.do(ctx, repo, http.MethodDelete, "/repository/branches/"+url.PathEscape(branch), nil, nil)
	if forge.Status(err) == http.StatusNotFound {
		return nil
	}
	return err
}

// ReviewEnforced says the main branch is protected and nobody may push to
// it: a change reaches it only through a merge request.
func (f *Forge) ReviewEnforced(ctx context.Context, repo string) (bool, error) {
	var b struct {
		Push []struct {
			Level int `json:"access_level"`
		} `json:"push_access_levels"`
	}
	err := f.do(ctx, repo, http.MethodGet, "/protected_branches/main", nil, &b)
	if forge.Status(err) == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, p := range b.Push {
		if p.Level != 0 {
			return false, nil
		}
	}
	return true, nil
}

// conclude turns a GitLab pipeline status into a run's status and conclusion.
func conclude(status string) (string, string) {
	switch status {
	case "success":
		return "completed", "success"
	case "failed":
		return "completed", "failure"
	case "canceled":
		return "completed", "cancelled"
	case "skipped":
		return "completed", "skipped"
	case "manual":
		// It ran what it runs by itself and now waits for a person's click:
		// it moves no more by itself, and it released nothing.
		return "completed", "manual"
	}
	// Still going: created, pending, running, canceling, and the rest.
	return status, ""
}

func (f *Forge) Runs(ctx context.Context, repo, sha string) ([]forge.Run, error) {
	var res []struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Source string `json:"source"`
		URL    string `json:"web_url"`
		Status string `json:"status"`
	}
	if err := f.do(ctx, repo, http.MethodGet, "/pipelines?per_page=100&sha="+url.QueryEscape(sha), nil, &res); err != nil {
		return nil, err
	}
	out := make([]forge.Run, 0, len(res))
	for _, p := range res {
		name := p.Name
		if name == "" {
			name = "pipeline (" + p.Source + ")"
		}
		status, conclusion := conclude(p.Status)
		out = append(out, forge.Run{ID: strconv.FormatInt(p.ID, 10), Name: name, URL: p.URL, Status: status, Conclusion: conclusion})
	}
	return out, nil
}

func (f *Forge) FailedJobs(ctx context.Context, repo string, run forge.Run) ([]forge.Job, error) {
	var jobs []struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Stage        string `json:"stage"`
		URL          string `json:"web_url"`
		AllowFailure bool   `json:"allow_failure"`
	}
	if err := f.do(ctx, repo, http.MethodGet, "/pipelines/"+run.ID+"/jobs?per_page=100&scope[]=failed", nil, &jobs); err != nil {
		return nil, err
	}
	var out []forge.Job
	for _, j := range jobs {
		if j.AllowFailure {
			continue
		}
		job := forge.Job{Name: j.Name, Stage: j.Stage, URL: j.URL}
		raw, err := f.raw(ctx, repo, http.MethodGet, fmt.Sprintf("/jobs/%d/trace", j.ID), nil)
		if err != nil {
			job.Log = fmt.Sprintf("its log could not be read: %v", err)
		} else {
			job.Log = forge.Tail(string(raw), 6000)
		}
		out = append(out, job)
	}
	return out, nil
}

func decode(raw []byte, out any, method, path string) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}
