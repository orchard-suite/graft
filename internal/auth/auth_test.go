package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/orchard-suite/platformkit/v3/door"
)

// serve runs one request through the middleware and returns the response
// and the person the handler saw.
func serve(t *testing.T, mode Mode, h http.Header) (*httptest.ResponseRecorder, *door.Person) {
	t.Helper()
	return serveWith(t, Middleware(mode, slog.New(slog.NewTextHandler(io.Discard, nil))), h)
}

func serveWith(t *testing.T, mw func(http.Handler) http.Handler, h http.Header) (*httptest.ResponseRecorder, *door.Person) {
	t.Helper()
	var seen *door.Person
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := door.From(r.Context())
		if p.Email == "" {
			t.Fatal("handler ran without a person")
		}
		seen = &p
	})
	req := httptest.NewRequest(http.MethodGet, "/api/requests", nil)
	req.Header = h
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	return rec, seen
}

func TestProxySignedIn(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-Email", "user@example.com")
	h.Set("X-Forwarded-Groups", "User, Admin,,")
	rec, p := serve(t, ModeProxy, h)
	if rec.Code != http.StatusOK || p == nil {
		t.Fatalf("status %d, principal %v", rec.Code, p)
	}
	want := door.Person{Email: "user@example.com", Roles: []string{"User", "Admin"}}
	if !reflect.DeepEqual(*p, want) {
		t.Errorf("principal %+v, want %+v", *p, want)
	}
}

func TestProxyNoGroups(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-Email", "user@example.com")
	rec, p := serve(t, ModeProxy, h)
	if rec.Code != http.StatusOK || p == nil || len(p.Roles) != 0 {
		t.Errorf("status %d, person %+v; want admitted with no roles", rec.Code, p)
	}
}

func TestProxyMissingEmailIs401(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-Groups", "User")
	rec, p := serve(t, ModeProxy, h)
	if rec.Code != http.StatusUnauthorized || p != nil {
		t.Fatalf("status %d, principal %v; want 401 and no handler", rec.Code, p)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body["error"] != "Sign in through the house first." {
		t.Errorf("body %v (%v), want the door's refusal as a JSON error", body, err)
	}
}

func TestNoneIsLocalUser(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-Email", "user@example.com")
	rec, p := serve(t, ModeNone, h)
	if rec.Code != http.StatusOK || p == nil || p.Email != LocalUser {
		t.Fatalf("status %d, principal %v; want %s", rec.Code, p, LocalUser)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"proxy": ModeProxy, "none": ModeNone, " proxy ": ModeProxy} {
		if got, err := ParseMode(in); err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "entra", "Proxy"} {
		if _, err := ParseMode(in); err == nil {
			t.Errorf("ParseMode(%q) accepted", in)
		}
	}
}

// apps is a token directory with one app, and the middleware over it.
func apps(t *testing.T) func(http.Handler) http.Handler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "acme-web"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme-admin"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Apps(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// appSeen runs one request through the token middleware and returns the
// status and the app the handler saw.
func appSeen(mw func(http.Handler) http.Handler, h http.Header) (int, string) {
	seen := ""
	req := httptest.NewRequest(http.MethodPost, "/v1/requests", nil)
	req.Header = h
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = App(r.Context()) })).ServeHTTP(rec, req)
	return rec.Code, seen
}

func TestAppsTakeTheirOwnTokenOnly(t *testing.T) {
	mw := apps(t)
	cases := map[string]struct {
		auth string
		code int
		app  string
	}{
		"one app's token":  {"Bearer s3cret", http.StatusOK, "acme-web"},
		"another's token":  {"Bearer other", http.StatusOK, "acme-admin"},
		"wrong token":      {"Bearer nope", http.StatusUnauthorized, ""},
		"no scheme":        {"s3cret", http.StatusUnauthorized, ""},
		"no header":        {"", http.StatusUnauthorized, ""},
		"an empty token":   {"Bearer ", http.StatusUnauthorized, ""},
		"a token and more": {"Bearer s3cret-and-more", http.StatusUnauthorized, ""},
	}
	for name, c := range cases {
		h := http.Header{}
		if c.auth != "" {
			h.Set("Authorization", c.auth)
		}
		if code, app := appSeen(mw, h); code != c.code || app != c.app {
			t.Errorf("%s: status %d app %q, want %d %q", name, code, app, c.code, c.app)
		}
	}
}

// The token port never trusts the front door's headers: an email header
// without a token is refused.
func TestAppsIgnoreFrontDoorHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-Email", "user@example.com")
	if code, _ := appSeen(apps(t), h); code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", code)
	}
}

func TestAppsWithNoDirectoryRefuseAll(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer anything")
	if code, _ := appSeen(Apps("", slog.New(slog.NewTextHandler(io.Discard, nil))), h); code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", code)
	}
}
