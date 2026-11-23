// Package github is the GitHub adapter of the forge crossing, over GitHub's
// REST API.
package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orchard-suite/graft/internal/forge"
)

// Forge speaks to one GitHub: github.com, or a GitHub Enterprise Server.
type Forge struct {
	web    string
	rest   *forge.REST
	tokens forge.Tokens

	mu sync.Mutex
	// users are the logins the tokens belong to, by token.
	users map[string]string
}

// APIBase is the REST API of the GitHub whose web address is web.
func APIBase(web string) string {
	web = strings.TrimRight(web, "/")
	if web == "https://github.com" {
		return "https://api.github.com"
	}
	return web + "/api/v3"
}

// New returns the adapter for the GitHub at web. tokens gives the token
// for a repository.
func New(web string, tokens forge.Tokens) *Forge {
	web = strings.TrimRight(web, "/")
	return &Forge{web: web, tokens: tokens, users: map[string]string{}, rest: &forge.REST{
		Base: APIBase(web),
		HTTP: &http.Client{Timeout: 60 * time.Second},
		Auth: func(req *http.Request, token string) {
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		},
	}}
}

// UseAPI points the adapter at another API root; tests use it.
func (f *Forge) UseAPI(base string) { f.rest.Base = strings.TrimRight(base, "/") }

func (f *Forge) Kind() string                { return "github" }
func (f *Forge) CloneURL(repo string) string { return f.web + "/" + repo + ".git" }
func (f *Forge) CommitURL(repo, sha string) string {
	return f.web + "/" + repo + "/commit/" + sha
}

func (f *Forge) Credential(ctx context.Context, in io.Reader, out io.Writer) error {
	return forge.Credential(ctx, f.web, "x-access-token", f.tokens, in, out)
}

func (f *Forge) do(ctx context.Context, repo, method, path string, in, out any) error {
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return err
	}
	return f.rest.Do(ctx, tok, method, "/repos/"+repo+path, in, out)
}

func (f *Forge) CreateIssue(ctx context.Context, repo, title, body string) (forge.Issue, error) {
	var res struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	err := f.do(ctx, repo, http.MethodPost, "/issues", map[string]string{"title": title, "body": body}, &res)
	return forge.Issue{Number: res.Number, URL: res.URL}, err
}

func (f *Forge) UpdateIssue(ctx context.Context, repo string, number int, body string) error {
	return f.do(ctx, repo, http.MethodPatch, fmt.Sprintf("/issues/%d", number), map[string]string{"body": body}, nil)
}

// Comments reads an issue's comments a page at a time, oldest first, and
// keeps those after after. A comment by a bot, such as a GitHub App, is not
// a person's; a token Graft signs in with as a user is told apart by
// that user.
func (f *Forge) Comments(ctx context.Context, repo string, number int, after int64) ([]forge.Comment, error) {
	me := f.me(ctx, repo)
	var out []forge.Comment
	for page := 1; page <= maxPages; page++ {
		var comments []struct {
			ID      int64  `json:"id"`
			Body    string `json:"body"`
			Created string `json:"created_at"`
			URL     string `json:"html_url"`
			User    struct {
				ID    int64  `json:"id"`
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"user"`
		}
		if err := f.do(ctx, repo, http.MethodGet, fmt.Sprintf("/issues/%d/comments?per_page=100&page=%d", number, page), nil, &comments); err != nil {
			return nil, err
		}
		for _, c := range comments {
			if c.ID <= after {
				continue
			}
			out = append(out, forge.Comment{ID: c.ID, Body: c.Body, At: c.Created, URL: c.URL,
				Author:  forge.User{ID: c.User.ID, Username: c.User.Login, Bot: c.User.Type == "Bot", Mine: me != "" && strings.EqualFold(c.User.Login, me)},
				Uploads: f.uploads(c.Body)})
		}
		if len(comments) < 100 {
			break
		}
	}
	return out, nil
}

// maxPages bounds how many pages of an issue's comments are read at once.
const maxPages = 20

// uploads are the files a comment links to that GitHub keeps for it, as
// GitHub writes the link when a file is attached. Graft cannot read
// them, and says so; they are named only for that.
func (f *Forge) uploads(body string) []forge.Upload {
	link := regexp.MustCompile(regexp.QuoteMeta(f.web) + `/user-attachments/(?:assets|files)/[^\s()\[\]<>"']+`)
	seen := map[string]bool{}
	var out []forge.Upload
	for _, u := range link.FindAllString(body, -1) {
		if seen[u] {
			continue
		}
		seen[u] = true
		name := path.Base(u)
		if !strings.Contains(u, "/files/") {
			name = "a file"
		}
		out = append(out, forge.Upload{Ref: u, Name: name})
	}
	return out
}

// Upload is not offered: GitHub's API reads no file attached to a comment.
func (f *Forge) Upload(context.Context, string, string, int64) ([]byte, error) {
	return nil, forge.ErrNoUploads
}

// me is the login a user token belongs to, or empty for a GitHub App's
// token, which comments as a bot. It is asked once a token.
func (f *Forge) me(ctx context.Context, repo string) string {
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return ""
	}
	f.mu.Lock()
	login, ok := f.users[tok]
	f.mu.Unlock()
	if ok {
		return login
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := f.rest.Do(ctx, tok, http.MethodGet, "/user", nil, &u); err != nil {
		u.Login = ""
	}
	f.mu.Lock()
	f.users[tok] = u.Login
	f.mu.Unlock()
	return u.Login
}

// Email is the public address a GitHub user shows on their profile.
func (f *Forge) Email(ctx context.Context, repo string, u forge.User) (string, error) {
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return "", err
	}
	var user struct {
		Email string `json:"email"`
	}
	if err := f.rest.Do(ctx, tok, http.MethodGet, "/users/"+url.PathEscape(u.Username), nil, &user); err != nil {
		return "", err
	}
	return strings.TrimSpace(user.Email), nil
}

func (f *Forge) CommentIssue(ctx context.Context, repo string, number int, body string) (forge.Posted, error) {
	var c struct {
		ID  int64  `json:"id"`
		URL string `json:"html_url"`
	}
	if err := f.do(ctx, repo, http.MethodPost, fmt.Sprintf("/issues/%d/comments", number), map[string]string{"body": body}, &c); err != nil {
		return forge.Posted{}, err
	}
	return forge.Posted{ID: c.ID, URL: c.URL}, nil
}

// EditComment writes a comment again. GitHub notifies nobody of an edit.
func (f *Forge) EditComment(ctx context.Context, repo string, _ int, id int64, body string) error {
	return f.do(ctx, repo, http.MethodPatch, fmt.Sprintf("/issues/comments/%d", id), map[string]string{"body": body}, nil)
}

// Attach is not offered: GitHub's API takes no file for a comment.
func (f *Forge) Attach(context.Context, string, string, string, []byte) (string, error) {
	return "", forge.ErrNoUploads
}

func (f *Forge) CloseIssue(ctx context.Context, repo string, number int) error {
	return f.do(ctx, repo, http.MethodPatch, fmt.Sprintf("/issues/%d", number), map[string]string{"state": "closed"}, nil)
}

func (f *Forge) OpenPR(ctx context.Context, repo, head, base, title, body string) (forge.PR, error) {
	var res struct {
		Number int    `json:"number"`
		URL    string `json:"html_url"`
	}
	err := f.do(ctx, repo, http.MethodPost, "/pulls", map[string]string{"head": head, "base": base, "title": title, "body": body}, &res)
	return forge.PR{Number: res.Number, URL: res.URL}, err
}

func (f *Forge) PR(ctx context.Context, repo string, number int) (forge.PRState, error) {
	var pr struct {
		State    string `json:"state"`
		Merged   bool   `json:"merged"`
		MergeSHA string `json:"merge_commit_sha"`
		Head     struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := f.do(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls/%d", number), nil, &pr); err != nil {
		return forge.PRState{}, err
	}
	st := forge.PRState{State: forge.PROpen, Head: pr.Head.SHA}
	switch {
	case pr.Merged:
		st.State, st.MergeSHA = forge.PRMerged, pr.MergeSHA
		return st, nil
	case pr.State == "closed":
		st.State = forge.PRClosed
		return st, nil
	}
	var reviews []struct {
		State    string `json:"state"`
		CommitID string `json:"commit_id"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := f.do(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls/%d/reviews?per_page=100", number), nil, &reviews); err != nil {
		return forge.PRState{}, err
	}
	// Each person's last word counts, and only on the commit it shows now.
	last := map[string]string{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED":
			if r.CommitID == pr.Head.SHA {
				last[r.User.Login] = r.State
			} else {
				delete(last, r.User.Login)
			}
		case "CHANGES_REQUESTED", "DISMISSED":
			last[r.User.Login] = r.State
		}
	}
	approved, blocked := false, false
	for _, s := range last {
		approved = approved || s == "APPROVED"
		blocked = blocked || s == "CHANGES_REQUESTED"
	}
	st.Approved = approved && !blocked
	return st, nil
}

func (f *Forge) MergePR(ctx context.Context, repo string, number int, sha string) error {
	var err error
	// A repository allows some merge methods and not others; 405 says this
	// one is not allowed.
	for _, method := range []string{"merge", "squash", "rebase"} {
		err = f.do(ctx, repo, http.MethodPut, fmt.Sprintf("/pulls/%d/merge", number), map[string]string{"sha": sha, "merge_method": method}, nil)
		if forge.Status(err) != http.StatusMethodNotAllowed {
			return err
		}
	}
	return err
}

func (f *Forge) ClosePR(ctx context.Context, repo string, number int, comment string) error {
	if comment != "" {
		if _, err := f.CommentIssue(ctx, repo, number, comment); err != nil {
			return err
		}
	}
	return f.do(ctx, repo, http.MethodPatch, fmt.Sprintf("/pulls/%d", number), map[string]string{"state": "closed"}, nil)
}

func (f *Forge) DeleteBranch(ctx context.Context, repo, branch string) error {
	err := f.do(ctx, repo, http.MethodDelete, "/git/refs/heads/"+branch, nil, nil)
	if s := forge.Status(err); s == http.StatusNotFound || s == http.StatusUnprocessableEntity {
		return nil
	}
	return err
}

func (f *Forge) ReviewEnforced(ctx context.Context, repo string) (bool, error) {
	var rules []struct {
		Type string `json:"type"`
	}
	err := f.do(ctx, repo, http.MethodGet, "/rules/branches/main", nil, &rules)
	if err != nil && forge.Status(err) != http.StatusNotFound {
		return false, err
	}
	for _, r := range rules {
		if r.Type == "pull_request" {
			return true, nil
		}
	}
	var prot struct {
		Reviews *struct{} `json:"required_pull_request_reviews"`
	}
	err = f.do(ctx, repo, http.MethodGet, "/branches/main/protection", nil, &prot)
	switch forge.Status(err) {
	case http.StatusNotFound, http.StatusForbidden:
		// Not protected, or a token that may not read the classic rules:
		// either way nothing here says a review is a must.
		return false, nil
	}
	return prot.Reviews != nil, err
}

func (f *Forge) Runs(ctx context.Context, repo, sha string) ([]forge.Run, error) {
	var res struct {
		Runs []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			URL        string `json:"html_url"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := f.do(ctx, repo, http.MethodGet, "/actions/runs?per_page=100&head_sha="+url.QueryEscape(sha), nil, &res); err != nil {
		return nil, err
	}
	out := make([]forge.Run, 0, len(res.Runs))
	for _, r := range res.Runs {
		out = append(out, forge.Run{ID: strconv.FormatInt(r.ID, 10), Name: r.Name, URL: r.URL, Status: r.Status, Conclusion: r.Conclusion})
	}
	return out, nil
}

func (f *Forge) FailedJobs(ctx context.Context, repo string, run forge.Run) ([]forge.Job, error) {
	var res struct {
		Jobs []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			URL        string `json:"html_url"`
			Conclusion string `json:"conclusion"`
		} `json:"jobs"`
	}
	if err := f.do(ctx, repo, http.MethodGet, "/actions/runs/"+run.ID+"/jobs?per_page=100&filter=latest", nil, &res); err != nil {
		return nil, err
	}
	tok, err := f.tokens(ctx, repo)
	if err != nil {
		return nil, err
	}
	var out []forge.Job
	for _, j := range res.Jobs {
		if j.Conclusion != "failure" && j.Conclusion != "timed_out" {
			continue
		}
		job := forge.Job{Name: j.Name, URL: j.URL}
		raw, err := f.rest.Raw(ctx, tok, http.MethodGet, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, j.ID), nil)
		if err != nil {
			job.Log = fmt.Sprintf("its log could not be read: %v", err)
		} else {
			job.Log = forge.Tail(string(raw), 6000)
		}
		out = append(out, job)
	}
	return out, nil
}
