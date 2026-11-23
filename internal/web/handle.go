package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/orchard-suite/graft/internal/desk"
	"github.com/orchard-suite/graft/internal/store"
)

// Handle builds the handle port: what the agent's own `graft` CLI calls
// from inside the sandbox. It listens on loopback only and trusts one thing,
// the secret of a request an agent is working on right now, which names
// that request and nothing else. It lends tools; it holds no key.
func (s *Server) Handle() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools", s.handled(s.handleTools))
	mux.HandleFunc("POST /tools/{id}/call", s.handled(s.handleCall))
	mux.HandleFunc("POST /evidence", s.handled(s.handleEvidence))
	mux.HandleFunc("GET /healthz", healthz)
	return s.logged(mux)
}

// handled finds the request a handle secret serves before the handler runs.
func (s *Server) handled(next func(http.ResponseWriter, *http.Request, *store.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		req, err := s.desk.ByHandle(r.Context(), secret)
		if errors.Is(err, desk.ErrNoHandle) {
			s.log.WarnContext(r.Context(), "handle refused", "route", r.Pattern)
			writeError(w, http.StatusUnauthorized, "no request is being worked under this handle")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		next(w, r, req)
	}
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request, req *store.Request) {
	list, err := s.desk.Tools(r.Context(), req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCall(w http.ResponseWriter, r *http.Request, req *store.Request) {
	var in struct {
		Env    string         `json:"env"`
		Inputs map[string]any `json:"inputs"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	res, err := s.desk.CallTool(r.Context(), req, r.PathValue("id"), in.Env, in.Inputs)
	if errors.Is(err, desk.ErrNoTool) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "tool call failed", "request", req.ID, "tool", r.PathValue("id"), "error", err)
		writeError(w, http.StatusBadGateway, "the tool could not be called")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEvidence(w http.ResponseWriter, r *http.Request, req *store.Request) {
	var in struct {
		Ref  string `json:"ref"`
		Note string `json:"note"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	n, err := s.desk.AddEvidence(r.Context(), req, in.Ref, in.Note)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int{"evidence": n})
}
