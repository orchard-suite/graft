package web

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// hookMax is the most of a webhook's body Graft reads.
const hookMax = 1 << 20

// Hook builds the forge's port: where the forge says someone commented on
// an issue. It trusts one thing, the secret the program's webhook carries,
// read from secretFile on every call so a rotated secret works at once. A
// webhook is a nudge and nothing more: who wrote what is read from the forge
// itself, with Graft's own credential.
func (s *Server) Hook(secretFile string) http.Handler {
	s.hookSecret = secretFile
	mux := http.NewServeMux()
	for _, rt := range s.hookRoutes() {
		mux.Handle(rt.pattern, rt.handler)
	}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "no such route")
	}))
	mux.HandleFunc("GET /healthz", healthz)
	return s.logged(mux)
}

// hookRoutes are the forge's port's routes.
func (s *Server) hookRoutes() []route {
	return []route{
		{"POST /hooks/gitlab", s.gitlabHook},
	}
}

// gitlabHook takes GitLab's note event. A comment on an issue that is a
// request's ticket has the ticket read again; anything else is answered and
// left alone, so GitLab never turns the webhook off for failing.
func (s *Server) gitlabHook(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(s.hookSecret)
	secret := strings.TrimSpace(string(raw))
	if err != nil || secret == "" {
		s.log.ErrorContext(r.Context(), "webhook not taken: the hook's secret could not be read", "error", err)
		writeError(w, http.StatusServiceUnavailable, "the hook's secret is not set")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Gitlab-Token")), []byte(secret)) != 1 {
		s.log.WarnContext(r.Context(), "webhook refused: it did not carry the hook's secret")
		refusals.Add(r.Context(), 1, metric.WithAttributes(attribute.String("reason", "hook_secret")))
		writeError(w, http.StatusUnauthorized, "this webhook does not carry the hook's secret")
		return
	}
	var ev struct {
		Kind    string `json:"object_kind"`
		Project struct {
			Path string `json:"path_with_namespace"`
		} `json:"project"`
		Note struct {
			On string `json:"noteable_type"`
		} `json:"object_attributes"`
		Issue struct {
			IID int `json:"iid"`
		} `json:"issue"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, hookMax)).Decode(&ev); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be GitLab's JSON")
		return
	}
	if ev.Kind != "note" || ev.Note.On != "Issue" || ev.Issue.IID == 0 {
		s.log.InfoContext(r.Context(), "webhook ignored: not a comment on an issue", "kind", ev.Kind, "on", ev.Note.On)
		writeJSON(w, http.StatusOK, map[string]bool{"heard": false})
		return
	}
	if !s.desk.Heard(r.Context(), ev.Project.Path, ev.Issue.IID) {
		writeJSON(w, http.StatusOK, map[string]bool{"heard": false})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"heard": true})
}
