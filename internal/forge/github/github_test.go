package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orchard-suite/graft/internal/forge"
)

type answer struct {
	status int
	body   string
}

func serve(t *testing.T, answers map[string]answer) (*Forge, map[string]map[string]any) {
	t.Helper()
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("%s %s came without the token", r.Method, r.URL.Path)
		}
		key := r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if prev, ok := bodies[key]; ok {
				body["previous_method"] = prev["merge_method"]
			}
			bodies[key] = body
		}
		a, ok := answers[key]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		if a.status != 0 {
			w.WriteHeader(a.status)
		}
		_, _ = w.Write([]byte(a.body))
	}))
	t.Cleanup(srv.Close)
	f := New("https://github.com", func(context.Context, string) (string, error) { return "tok", nil })
	f.UseAPI(srv.URL)
	return f, bodies
}

func TestAPIBase(t *testing.T) {
	if got := APIBase("https://github.com/"); got != "https://api.github.com" {
		t.Errorf("github.com: %s", got)
	}
	if got := APIBase("https://github.example.com"); got != "https://github.example.com/api/v3" {
		t.Errorf("enterprise: %s", got)
	}
}

func TestPullRequestApproval(t *testing.T) {
	ctx := context.Background()
	open := `{"state":"open","head":{"sha":"h2"}}`
	cases := []struct {
		name, reviews string
		want          bool
	}{
		{"nobody reviewed", `[]`, false},
		{"approved on this head", `[{"state":"APPROVED","commit_id":"h2","user":{"login":"a"}}]`, true},
		{"approved on an older head", `[{"state":"APPROVED","commit_id":"h1","user":{"login":"a"}}]`, false},
		{"one approves, one asks for changes", `[{"state":"APPROVED","commit_id":"h2","user":{"login":"a"}},{"state":"CHANGES_REQUESTED","commit_id":"h2","user":{"login":"b"}}]`, false},
		{"asked for changes, then approved", `[{"state":"CHANGES_REQUESTED","commit_id":"h1","user":{"login":"a"}},{"state":"APPROVED","commit_id":"h2","user":{"login":"a"}}]`, true},
	}
	for _, c := range cases {
		f, _ := serve(t, map[string]answer{
			"GET /repos/acme/api/pulls/4":         {body: open},
			"GET /repos/acme/api/pulls/4/reviews": {body: c.reviews},
		})
		got, err := f.PR(ctx, "acme/api", 4)
		if err != nil || got.Approved != c.want || got.State != forge.PROpen || got.Head != "h2" {
			t.Errorf("%s: %+v %v", c.name, got, err)
		}
	}
	f, _ := serve(t, map[string]answer{"GET /repos/acme/api/pulls/4": {body: `{"state":"closed","merged":true,"merge_commit_sha":"m1","head":{"sha":"h2"}}`}})
	if got, err := f.PR(ctx, "acme/api", 4); err != nil || got.State != forge.PRMerged || got.MergeSHA != "m1" {
		t.Errorf("merged: %+v %v", got, err)
	}
}

func TestMergeTriesTheMethodsTheRepositoryAllows(t *testing.T) {
	f, bodies := serve(t, map[string]answer{"PUT /repos/acme/api/pulls/4/merge": {status: http.StatusMethodNotAllowed, body: `{"message":"Merge commits are not allowed"}`}})
	err := f.MergePR(context.Background(), "acme/api", 4, "h2")
	if forge.Status(err) != http.StatusMethodNotAllowed {
		t.Fatalf("want the last refusal, got %v", err)
	}
	last := bodies["PUT /repos/acme/api/pulls/4/merge"]
	if last["merge_method"] != "rebase" || last["sha"] != "h2" {
		t.Errorf("the last try: %v", last)
	}
}

func TestReviewEnforced(t *testing.T) {
	ctx := context.Background()
	f, _ := serve(t, map[string]answer{"GET /repos/acme/api/rules/branches/main": {body: `[{"type":"deletion"},{"type":"pull_request"}]`}})
	if got, err := f.ReviewEnforced(ctx, "acme/api"); err != nil || !got {
		t.Errorf("a ruleset asks for a pull request: %v %v", got, err)
	}
	f, _ = serve(t, map[string]answer{
		"GET /repos/acme/api/rules/branches/main":      {body: `[]`},
		"GET /repos/acme/api/branches/main/protection": {body: `{"required_pull_request_reviews":{"required_approving_review_count":1}}`},
	})
	if got, err := f.ReviewEnforced(ctx, "acme/api"); err != nil || !got {
		t.Errorf("classic protection asks for a review: %v %v", got, err)
	}
	f, _ = serve(t, map[string]answer{"GET /repos/acme/api/rules/branches/main": {body: `[]`}})
	if got, err := f.ReviewEnforced(ctx, "acme/api"); err != nil || got {
		t.Errorf("nothing asks for a review: %v %v", got, err)
	}
}

func TestCommentsKnowABotFromAPerson(t *testing.T) {
	ctx := context.Background()
	f, bodies := serve(t, map[string]answer{
		"GET /user": {body: `{"login": "graft-user"}`},
		"GET /repos/acme/api/issues/7/comments": {body: `[
			{"id": 1, "body": "old", "user": {"id": 5, "login": "lead", "type": "User"}},
			{"id": 2, "body": "Round 2 is ready", "html_url": "https://github.com/acme/api/issues/7#issuecomment-2", "user": {"id": 6, "login": "graft[bot]", "type": "Bot"}},
			{"id": 3, "body": "mine too", "user": {"id": 7, "login": "graft-user", "type": "User"}},
			{"id": 4, "body": "Keep the old line.", "html_url": "https://github.com/acme/api/issues/7#issuecomment-4", "user": {"id": 5, "login": "lead", "type": "User"}}
		]`},
		"GET /users/lead":                {body: `{"login": "lead", "email": "lead@example.com"}`},
		"PATCH /repos/acme/api/issues/7": {body: `{"number": 7}`},
	})
	got, err := f.Comments(ctx, "acme/api", 7, 1)
	if err != nil || len(got) != 3 {
		t.Fatalf("comments: %+v %v", got, err)
	}
	if !got[0].Author.Bot || !got[1].Author.Mine || got[2].Author.Bot || got[2].Author.Mine || got[2].URL == "" {
		t.Errorf("comments: %+v", got)
	}
	if email, err := f.Email(ctx, "acme/api", got[2].Author); err != nil || email != "lead@example.com" {
		t.Errorf("email %q %v", email, err)
	}
	if err := f.UpdateIssue(ctx, "acme/api", 7, "round 2"); err != nil || bodies["PATCH /repos/acme/api/issues/7"]["body"] != "round 2" {
		t.Errorf("update: %v %v", err, bodies)
	}
}

func TestACommentsFileIsNamedButNeverRead(t *testing.T) {
	f := New("https://github.com", func(context.Context, string) (string, error) { return "tok", nil })
	got := f.uploads("![logo](https://github.com/user-attachments/assets/1f2e-33) and [spec.pdf](https://github.com/user-attachments/files/123/spec.pdf) but not https://evil.example.com/user-attachments/assets/9")
	if len(got) != 2 || got[0].Name != "a file" || got[1].Name != "spec.pdf" {
		t.Fatalf("uploads %+v", got)
	}
	if _, err := f.Upload(context.Background(), "acme/api", got[1].Ref, 100); !errors.Is(err, forge.ErrNoUploads) {
		t.Errorf("upload: %v", err)
	}
}
