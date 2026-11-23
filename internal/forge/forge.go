// Package forge is the crossing to where repositories live. Graft asks a
// forge for the same few things whichever one it is: where to clone from
// and with what credential, issues, pull requests, whether the main branch
// makes a review a must, and how the pipeline of a commit went.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// Forge is one forge, as Graft uses it. A repository is named by its
// path there, such as acme/api.
type Forge interface {
	// Kind is the forge's name: github or gitlab.
	Kind() string
	// CloneURL is where git clones the repository from, over HTTPS.
	CloneURL(repo string) string
	// CommitURL is the page of one commit.
	CommitURL(repo, sha string) string
	// Credential answers git's credential helper protocol with a token for
	// the repository git asks about. A request for another host gets no
	// answer.
	Credential(ctx context.Context, in io.Reader, out io.Writer) error

	CreateIssue(ctx context.Context, repo, title, body string) (Issue, error)
	// UpdateIssue writes an issue's description again.
	UpdateIssue(ctx context.Context, repo string, number int, body string) error
	// CommentIssue writes a new comment on an issue, and says where it is.
	CommentIssue(ctx context.Context, repo string, number int, body string) (Posted, error)
	// EditComment writes one of Graft's comments on an issue again.
	EditComment(ctx context.Context, repo string, number int, id int64, body string) error
	// Attach gives the forge a file to keep for the repository, as a person
	// attaches a picture to a comment, and answers the address a comment
	// links to it by. A forge that takes none from Graft answers
	// ErrNoUploads.
	Attach(ctx context.Context, repo, name, contentType string, data []byte) (string, error)
	// Comments lists the comments people and bots wrote on an issue after
	// the one with id after, oldest first. What the forge itself writes on
	// an issue, such as a note that it was closed, is left out. Each names
	// the files it links to that the forge keeps for the repository.
	Comments(ctx context.Context, repo string, number int, after int64) ([]Comment, error)
	// Upload reads a file a comment linked to, by its Ref, through the
	// forge's API for the repository and never from the link itself: at
	// most max bytes of it. A forge that does not let Graft read one
	// answers ErrNoUploads.
	Upload(ctx context.Context, repo, ref string, max int64) ([]byte, error)
	// Email is the address a forge user shows on their profile, or empty.
	Email(ctx context.Context, repo string, u User) (string, error)
	CloseIssue(ctx context.Context, repo string, number int) error

	OpenPR(ctx context.Context, repo, head, base, title, body string) (PR, error)
	PR(ctx context.Context, repo string, number int) (PRState, error)
	// MergePR asks the forge to merge the pull request while its head is
	// still sha. The forge's own branch rules may refuse.
	MergePR(ctx context.Context, repo string, number int, sha string) error
	ClosePR(ctx context.Context, repo string, number int, comment string) error
	DeleteBranch(ctx context.Context, repo, branch string) error
	// ReviewEnforced says whether the forge itself keeps a change off the
	// main branch until a pull request is reviewed.
	ReviewEnforced(ctx context.Context, repo string) (bool, error)

	// Runs lists the pipeline runs the forge started for a commit.
	Runs(ctx context.Context, repo, sha string) ([]Run, error)
	// FailedJobs are a run's jobs that failed, each with its page and the
	// end of its log. A job allowed to fail is not one.
	FailedJobs(ctx context.Context, repo string, run Run) ([]Job, error)
}

// Job is one job of a pipeline run that failed: its name, its stage where
// the forge has stages, its page, and the end of its log.
type Job struct {
	Name  string
	Stage string
	URL   string
	Log   string
}

type Issue struct {
	Number int
	URL    string
}

// Posted is a comment Graft wrote: its id, and its page.
type Posted struct {
	ID  int64
	URL string
}

// Comment is one comment on an issue.
type Comment struct {
	ID     int64
	Body   string
	At     string
	URL    string
	Author User
	// Uploads are the files the comment links to that the forge keeps for
	// the repository, as people attach a picture or a PDF to a comment. A
	// link anywhere else is not one.
	Uploads []Upload
}

// Upload is one file a comment links to: Ref names it to the forge's
// Upload, and Name is its file name, as the link gives it.
type Upload struct {
	Ref  string
	Name string
}

// ErrNoUploads is a forge that does not let Graft read a file attached
// to a comment, or attach one.
var ErrNoUploads = errors.New("this forge does not let Graft read a file attached to a comment")

// User is who wrote a comment, as the forge names them.
type User struct {
	ID       int64
	Username string
	// Bot says a machine wrote it, not a person.
	Bot bool
	// Mine says Graft wrote it, with the credential it holds.
	Mine bool
}

type PR struct {
	Number int
	URL    string
}

// States of a pull request.
const (
	PROpen   = "open"
	PRMerged = "merged"
	PRClosed = "closed"
)

type PRState struct {
	State string
	// Approved says a person approved it and nobody asks for changes.
	Approved bool
	// Head is the commit it shows; MergeSHA is the commit main got.
	Head     string
	MergeSHA string
}

// Run is one pipeline run for a commit, in the forge's own words made one:
// Status is "completed" once it will move no more by itself, and
// Conclusion is then success, failure, cancelled, skipped, manual (it waits
// for a person's click), or another word the forge uses.
type Run struct {
	ID         string
	Name       string
	URL        string
	Status     string
	Conclusion string
}

// Done says the run will move no more by itself.
func (r Run) Done() bool { return r.Status == "completed" }

// Green says the run passed. Only success is: a run that was cancelled,
// skipped, failed, or waits for a click released nothing.
func (r Run) Green() bool { return r.Done() && r.Conclusion == "success" }

// Tokens gives the token that reaches one repository.
type Tokens func(ctx context.Context, repo string) (string, error)

// GitEnv is the environment that makes every git started under it ask
// helper for the forge's credentials, with the repository's path. helper is
// a git credential helper command line; forgeURL is the forge's address.
func GitEnv(forgeURL, helper string) []string {
	base := strings.TrimRight(forgeURL, "/")
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential." + base + ".helper",
		"GIT_CONFIG_VALUE_0=" + helper,
		"GIT_CONFIG_KEY_1=credential." + base + ".useHttpPath",
		"GIT_CONFIG_VALUE_1=true",
	}
}

// Credential answers git's credential helper protocol for the forge at
// forgeURL: given what git wrote on in, it writes user and a token for the
// repository at the path. A request for another host gets no answer, and
// git goes on without one.
func Credential(ctx context.Context, forgeURL, user string, tokens Tokens, in io.Reader, out io.Writer) error {
	base, err := url.Parse(forgeURL)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			fields[k] = v
		}
	}
	if fields["protocol"] != base.Scheme || fields["host"] != base.Host {
		return nil
	}
	repo := strings.TrimSuffix(strings.Trim(fields["path"], "/"), ".git")
	if !strings.Contains(repo, "/") {
		return fmt.Errorf("forge: git asked for %q, not a repository path; set credential.useHttpPath", fields["path"])
	}
	tok, err := tokens(ctx, repo)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "username=%s\npassword=%s\n", user, tok)
	return err
}

// Error is a forge that answered with a failure status.
type Error struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s %s: %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Status is the HTTP status in err when it is a forge's answer, else 0.
func Status(err error) int {
	if e, ok := err.(*Error); ok {
		return e.Status
	}
	return 0
}

// REST calls a forge's JSON API.
type REST struct {
	// Base is the API's root, with no trailing slash.
	Base string
	HTTP *http.Client
	// Auth sets the request's credential.
	Auth func(req *http.Request, token string)
}

// Do sends one call with the token and decodes a JSON answer into out when
// out is not nil. An answer of 300 or more is an *Error.
func (c *REST) Do(ctx context.Context, token, method, path string, in, out any) error {
	raw, err := c.Raw(ctx, token, method, path, in)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// Raw sends one call and returns the answer's body, at most 4 MB of it.
func (c *REST) Raw(ctx context.Context, token, method, path string, in any) ([]byte, error) {
	return c.send(ctx, token, method, path, in, 4<<20)
}

// Send posts one file to the API as a multipart form's field, and decodes
// the JSON answer into out.
func (c *REST) Send(ctx context.Context, token, path, field, name, contentType string, data []byte, out any) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, name))
	h.Set("Content-Type", contentType)
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	c.Auth(req, token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		return &Error{Method: http.MethodPost, Path: path, Status: res.StatusCode, Body: clipBody(raw)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	return nil
}

// clipBody is a forge's failure answer, short enough for an error.
func clipBody(raw []byte) string {
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return msg
}

// Fetch reads one file from the API: at most max bytes of its body.
func (c *REST) Fetch(ctx context.Context, token, path string, max int64) ([]byte, error) {
	return c.send(ctx, token, http.MethodGet, path, nil, max)
}

func (c *REST) send(ctx context.Context, token, method, path string, in any, max int64) ([]byte, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.Auth(req, token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, max))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		return nil, &Error{Method: method, Path: path, Status: res.StatusCode, Body: clipBody(raw)}
	}
	return raw, nil
}

// KeepOnHost is a redirect policy that sends header, a credential, only to
// the host the call was made to. Go drops its own credential headers on a
// redirect to another host, but not one a forge names itself.
func KeepOnHost(header string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del(header)
		}
		return nil
	}
}

// Tail is the last n bytes of s, cut at a line boundary.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
