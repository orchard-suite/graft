package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orchard-suite/graft/internal/forge"
)

// fake is a GitLab that answers from a table of "METHOD escaped-path" and
// records what it was sent.
type fake struct {
	t       *testing.T
	answers map[string]string
	bodies  map[string]map[string]any
}

func serve(t *testing.T, answers map[string]string) (*Forge, *fake) {
	t.Helper()
	fk := &fake{t: t, answers: answers, bodies: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("%s %s came without the token", r.Method, r.URL.Path)
		}
		key := r.Method + " " + r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			fk.bodies[key] = body
		}
		answer, ok := answers[key]
		if !ok {
			http.Error(w, `{"message":"404 Not Found"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	f := New(srv.URL, func(context.Context, string) (string, error) { return "tok", nil })
	return f, fk
}

const project = "/api/v4/projects/acme%2Fgroup%2Fapi"

func TestIssueAndMergeRequest(t *testing.T) {
	ctx := context.Background()
	f, fk := serve(t, map[string]string{
		"POST " + project + "/issues":         `{"iid": 7, "web_url": "https://gitlab.example.com/acme/group/api/-/issues/7"}`,
		"POST " + project + "/merge_requests": `{"iid": 3, "web_url": "https://gitlab.example.com/acme/group/api/-/merge_requests/3"}`,
	})
	issue, err := f.CreateIssue(ctx, "acme/group/api", "The page is blank", "body")
	if err != nil || issue.Number != 7 || !strings.HasSuffix(issue.URL, "/issues/7") {
		t.Fatalf("issue: %+v %v", issue, err)
	}
	if got := fk.bodies["POST "+project+"/issues"]; got["title"] != "The page is blank" || got["description"] != "body" {
		t.Errorf("issue body: %v", got)
	}
	pr, err := f.OpenPR(ctx, "acme/group/api", "graft/abc", "main", "fix", "why")
	if err != nil || pr.Number != 3 {
		t.Fatalf("merge request: %+v %v", pr, err)
	}
	if got := fk.bodies["POST "+project+"/merge_requests"]; got["source_branch"] != "graft/abc" || got["target_branch"] != "main" {
		t.Errorf("merge request body: %v", got)
	}
}

func TestMergeRequestState(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, mr, approvals string
		want                forge.PRState
	}{
		{"open, nobody approved", `{"state":"opened","sha":"h1"}`, `{"approved":true,"approved_by":[]}`, forge.PRState{State: forge.PROpen, Head: "h1"}},
		{"open, a person approved", `{"state":"opened","sha":"h1"}`, `{"approved":true,"approved_by":[{"user":{"id":1}}]}`, forge.PRState{State: forge.PROpen, Head: "h1", Approved: true}},
		{"merged with a merge commit", `{"state":"merged","sha":"h1","merge_commit_sha":"m1"}`, ``, forge.PRState{State: forge.PRMerged, Head: "h1", MergeSHA: "m1"}},
		{"merged fast-forward", `{"state":"merged","sha":"h1"}`, ``, forge.PRState{State: forge.PRMerged, Head: "h1", MergeSHA: "h1"}},
		{"closed", `{"state":"closed","sha":"h1"}`, ``, forge.PRState{State: forge.PRClosed, Head: "h1"}},
	}
	for _, c := range cases {
		f, _ := serve(t, map[string]string{
			"GET " + project + "/merge_requests/3":           c.mr,
			"GET " + project + "/merge_requests/3/approvals": c.approvals,
		})
		got, err := f.PR(ctx, "acme/group/api", 3)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v %v", c.name, got, err)
		}
	}
}

func TestReviewEnforced(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		answer string
		want   bool
	}{
		"nobody may push":      {`{"push_access_levels":[{"access_level":0}]}`, true},
		"maintainers may push": {`{"push_access_levels":[{"access_level":40}]}`, false},
	} {
		f, _ := serve(t, map[string]string{"GET " + project + "/protected_branches/main": c.answer})
		if got, err := f.ReviewEnforced(ctx, "acme/group/api"); err != nil || got != c.want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	f, _ := serve(t, nil)
	if got, err := f.ReviewEnforced(ctx, "acme/group/api"); err != nil || got {
		t.Errorf("an unprotected branch: %v %v", got, err)
	}
}

func TestRunsAndFailedJobs(t *testing.T) {
	ctx := context.Background()
	f, _ := serve(t, map[string]string{
		"GET " + project + "/pipelines?per_page=100&sha=abc":                `[{"id":11,"source":"push","web_url":"u11","status":"failed"},{"id":12,"name":"deploy","web_url":"u12","status":"running"},{"id":13,"web_url":"u13","status":"canceled"},{"id":14,"web_url":"u14","status":"manual"},{"id":15,"web_url":"u15","status":"canceling"},{"id":16,"web_url":"u16","status":"success"}]`,
		"GET " + project + "/pipelines/11/jobs?per_page=100&scope[]=failed": `[{"id":21,"name":"test","stage":"test","web_url":"j21"},{"id":22,"name":"lint","stage":"test","allow_failure":true}]`,
		"GET " + project + "/jobs/21/trace":                                 "ok 1\nFAIL TestOrders\n",
	})
	runs, err := f.Runs(ctx, "acme/group/api", "abc")
	if err != nil || len(runs) != 6 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	if runs[0].Green() || !runs[0].Done() || runs[0].Name != "pipeline (push)" || runs[1].Done() {
		t.Errorf("a failed run and a running one: %+v", runs[:2])
	}
	// Only success released anything: a cancelled run, and one that waits
	// for a click, are over and not green; one still cancelling goes on.
	if runs[2].Green() || !runs[2].Done() || runs[3].Green() || !runs[3].Done() || runs[3].Conclusion != "manual" || runs[4].Done() || !runs[5].Green() {
		t.Errorf("runs: %+v", runs[2:])
	}
	jobs, err := f.FailedJobs(ctx, "acme/group/api", runs[0])
	if err != nil || len(jobs) != 1 {
		t.Fatalf("failed jobs: %+v %v", jobs, err)
	}
	if j := jobs[0]; j.Name != "test" || j.Stage != "test" || j.URL != "j21" || !strings.Contains(j.Log, "FAIL TestOrders") {
		t.Errorf("failed job: %+v", j)
	}
}

func TestCommentsLeaveOutSystemNotesAndKnowGraftsOwn(t *testing.T) {
	ctx := context.Background()
	notes := `[
		{"id": 10, "body": "older", "system": false, "author": {"id": 5, "username": "lead"}},
		{"id": 11, "body": "changed the description", "system": true, "author": {"id": 5, "username": "lead"}},
		{"id": 12, "body": "Round 2 is ready", "system": false, "author": {"id": 99, "username": "project_1_bot_abc"}},
		{"id": 13, "body": "Keep the old line.", "system": false, "created_at": "2026-11-18T15:00:00Z", "author": {"id": 5, "username": "lead"}}
	]`
	f, fk := serve(t, map[string]string{
		"GET /api/v4/user": `{"id": 99}`,
		"GET " + project + "/issues/7/notes?sort=asc&order_by=created_at&per_page=100&page=1": notes,
		"GET /api/v4/users/5":          `{"id": 5, "public_email": "lead@example.com"}`,
		"PUT " + project + "/issues/7": `{"iid": 7}`,
	})
	got, err := f.Comments(ctx, "acme/group/api", 7, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("comments: %+v %v", got, err)
	}
	if !got[0].Author.Mine || got[1].Author.Mine || got[1].Body != "Keep the old line." || got[1].Author.Username != "lead" || got[1].At != "2026-11-18T15:00:00Z" {
		t.Errorf("comments: %+v", got)
	}
	if !strings.HasSuffix(got[1].URL, "/acme/group/api/-/issues/7#note_13") {
		t.Errorf("url %s", got[1].URL)
	}
	if email, err := f.Email(ctx, "acme/group/api", got[1].Author); err != nil || email != "lead@example.com" {
		t.Errorf("email %q %v", email, err)
	}
	if err := f.UpdateIssue(ctx, "acme/group/api", 7, "round 2"); err != nil || fk.bodies["PUT "+project+"/issues/7"]["description"] != "round 2" {
		t.Errorf("update: %v %v", err, fk.bodies)
	}
}

func TestCommentsNameOnlyTheProjectsOwnUploads(t *testing.T) {
	ctx := context.Background()
	answers := map[string]string{"GET /api/v4/user": `{"id": 1}`}
	f, _ := serve(t, answers)
	a, b := strings.Repeat("ab", 16), strings.Repeat("0f", 16)
	body := strings.Join([]string{
		"Use this logo: ![logo](/uploads/" + a + "/logo%20big.png)",
		"and the spec [spec](" + f.web + "/acme/group/api/uploads/" + b + "/spec.pdf).",
		"Not these: ![x](https://evil.example.com/uploads/" + a + "/x.png)",
		"![y](" + f.web + "/acme/group/other/uploads/" + a + "/y.png)",
		"![z](/uploads/nothex/z.png) ![w](/uploads/" + a[:30] + "/w.png)",
		"see https://evil.example.com/acme/group/api/uploads/" + a + "/v.png and http://127.0.0.1/?u=/uploads/" + a + "/u.png",
		"![dots](/uploads/" + a + "/..) ![slash](/uploads/" + a + "/a%2Fb.png)",
		"Again: ![logo](/uploads/" + a + "/logo%20big.png)",
	}, "\n")
	notes, _ := json.Marshal([]map[string]any{{"id": 5, "body": body, "author": map[string]any{"id": 9, "username": "lead"}}})
	answers["GET "+project+"/issues/7/notes?sort=asc&order_by=created_at&per_page=100&page=1"] = string(notes)
	comments, err := f.Comments(ctx, "acme/group/api", 7, 0)
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments %+v %v", comments, err)
	}
	want := []forge.Upload{{Ref: a + "/logo big.png", Name: "logo big.png"}, {Ref: b + "/spec.pdf", Name: "spec.pdf"}}
	if got := comments[0].Uploads; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("uploads %+v, want %+v", got, want)
	}
}

func TestAnUploadIsReadThroughTheProjectsAPIAndCapped(t *testing.T) {
	ctx := context.Background()
	a := strings.Repeat("ab", 16)
	f, _ := serve(t, map[string]string{"GET " + project + "/uploads/" + a + "/logo%20big.png": "\x89PNG the logo"})
	data, err := f.Upload(ctx, "acme/group/api", a+"/logo big.png", 100)
	if err != nil || string(data) != "\x89PNG the logo" {
		t.Fatalf("upload %q %v", data, err)
	}
	if data, err := f.Upload(ctx, "acme/group/api", a+"/logo big.png", 5); err != nil || len(data) != 5 {
		t.Errorf("a capped read gave %d bytes: %v", len(data), err)
	}
	for _, ref := range []string{"../../user", a + "/../x", "nothex/x.png", a + "/", a} {
		if _, err := f.Upload(ctx, "acme/group/api", ref, 100); err == nil || !strings.Contains(err.Error(), "names no uploaded file") {
			t.Errorf("ref %q: %v", ref, err)
		}
	}
}

func TestTheTokenNeverFollowsARedirectToAnotherHost(t *testing.T) {
	var leaked bool
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("PRIVATE-TOKEN") != ""
		_, _ = w.Write([]byte("\x89PNG from the object store"))
	}))
	t.Cleanup(store.Close)
	gl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, store.URL+"/signed/logo.png", http.StatusFound)
	}))
	t.Cleanup(gl.Close)
	f := New(gl.URL, func(context.Context, string) (string, error) { return "tok", nil })
	data, err := f.Upload(context.Background(), "acme/api", strings.Repeat("ab", 16)+"/logo.png", 100)
	if err != nil || string(data) != "\x89PNG from the object store" {
		t.Fatalf("upload %q %v", data, err)
	}
	if leaked {
		t.Error("the token went to the host GitLab redirected to")
	}
}

func TestARunsCommentIsWrittenOnceAndThenEdited(t *testing.T) {
	ctx := context.Background()
	f, fk := serve(t, map[string]string{
		"POST " + project + "/issues/7/notes":     `{"id": 4401}`,
		"PUT " + project + "/issues/7/notes/4401": `{"id": 4401}`,
	})
	posted, err := f.CommentIssue(ctx, "acme/group/api", 7, "**Build** running")
	if err != nil || posted.ID != 4401 || !strings.HasSuffix(posted.URL, "/acme/group/api/-/issues/7#note_4401") {
		t.Fatalf("posted: %+v %v", posted, err)
	}
	if err := f.EditComment(ctx, "acme/group/api", 7, posted.ID, "**Build** done"); err != nil {
		t.Fatal(err)
	}
	if got := fk.bodies["PUT "+project+"/issues/7/notes/4401"]; got["body"] != "**Build** done" {
		t.Errorf("the edit: %v", got)
	}
}

func TestAFileIsAttachedThroughTheProjectsUploads(t *testing.T) {
	var name, kind string
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != project+"/uploads" || r.Header.Get("PRIVATE-TOKEN") != "tok" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		file, head, err := r.FormFile("file")
		if err != nil {
			t.Errorf("no file: %v", err)
			return
		}
		got, _ = io.ReadAll(file)
		name, kind = head.Filename, head.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"alt":"form-after","url":"/uploads/0123456789abcdef0123456789abcdef/form-after.png","markdown":"![form-after](/uploads/0123456789abcdef0123456789abcdef/form-after.png)"}`))
	}))
	t.Cleanup(srv.Close)
	f := New(srv.URL, func(context.Context, string) (string, error) { return "tok", nil })
	u, err := f.Attach(context.Background(), "acme/group/api", "form-after.png", "image/png", []byte("\x89PNG\r\n\x1a\npixels"))
	if err != nil || u != "/uploads/0123456789abcdef0123456789abcdef/form-after.png" {
		t.Fatalf("attached: %q %v", u, err)
	}
	if name != "form-after.png" || kind != "image/png" || string(got) != "\x89PNG\r\n\x1a\npixels" {
		t.Errorf("sent %q %q %q", name, kind, got)
	}
	if _, err := f.Attach(context.Background(), "acme/group/api", "../x.png", "image/png", got); err == nil {
		t.Error("a name with a folder in it was sent")
	}
}
