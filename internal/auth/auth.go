// Package auth says who is calling. In proxy mode the estate's front door
// signs the person in, and a door gate reads who they are; Graft trusts
// the door's headers because only the front door can reach that port. In
// none mode every request is one fixed local user. Both leave the caller as
// a door.Person, read with door.From. An intake app presents its own bearer
// token on a port of its own, and is read with App.
package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/orchard-suite/platformkit/v3/door"
)

type Mode string

const (
	// ModeProxy trusts the identity headers the front door sets.
	ModeProxy Mode = "proxy"
	// ModeNone signs every request in as LocalUser. Local development only.
	ModeNone Mode = "none"
)

// LocalUser is who every request is in ModeNone.
const LocalUser = "local@localhost"

// ParseMode reads GRAFT_AUTH_MODE. There is no default: a deployment
// must say which one it is.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.TrimSpace(s)); m {
	case ModeProxy, ModeNone:
		return m, nil
	case "":
		return "", fmt.Errorf("GRAFT_AUTH_MODE is required: %q or %q", ModeProxy, ModeNone)
	default:
		return "", fmt.Errorf("GRAFT_AUTH_MODE %q: want %q or %q", s, ModeProxy, ModeNone)
	}
}

// Middleware admits the page's requests. In proxy mode it admits anyone the
// front door signed in, whatever their roles, and answers 401 with a JSON
// error the page shows when the door named nobody.
func Middleware(mode Mode, log *slog.Logger) func(http.Handler) http.Handler {
	if mode == ModeNone {
		return as(LocalUser)
	}
	// A gate with no roles has nothing to refuse at construction.
	gate, _ := door.NewGate(log)
	gate.Refuse = func(w http.ResponseWriter, _ *http.Request, status int, message string) {
		writeError(w, status, message)
	}
	return gate.Wrap
}

// as signs every request in as email.
func as(email string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(door.With(r.Context(), door.Person{Email: email})))
		})
	}
}

type appKey struct{}

// App is the intake app whose token a request on the token port carried.
func App(ctx context.Context) string {
	app, _ := ctx.Value(appKey{}).(string)
	return app
}

// Apps admits only requests carrying an intake app's token as
// `Authorization: Bearer <token>`, and leaves the app's name on the
// request. dir holds one file per app, named for the app, whose content is
// its token; it is read on every request, so a rotated token works at once.
// It never reads the front door's headers.
func Apps(dir string, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			app := ""
			if ok && got != "" {
				app = appFor(dir, got, log)
			}
			if app == "" {
				log.WarnContext(r.Context(), "token refused", "path", r.URL.Path)
				writeError(w, http.StatusUnauthorized, "a valid bearer token is required")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), appKey{}, app)))
		})
	}
}

// appFor is the app whose token file holds token, or empty. Every file is
// compared, so how long a refusal takes says nothing about which app came
// close.
func appFor(dir, token string, log *slog.Logger) string {
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Error("intake token directory not read", "error", err)
		return ""
	}
	found := ""
	for _, e := range entries {
		// A mounted secret keeps its data under dot-named entries.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		want := strings.TrimSpace(string(raw))
		if want != "" && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1 {
			found = e.Name()
		}
	}
	return found
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
