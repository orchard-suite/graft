// Package workspace is where the agent's clones live and where every command
// that reads one runs: the gate's git and `make check`, and the git that moves
// commits in and out of a clone. Its container holds no credential.
//
// The runner never runs anything in an agent's clone itself. A hook, a git
// config, or a Makefile the agent wrote runs here, beside the agent, and never
// where the push key and the GitHub token are. Commits cross between the
// runner's own clone and the agent's as git bundles, which are data.
//
// Beside the runner in one pod, the server listens on the pod's loopback
// only and needs no secret: anything that can reach it can already run code
// in the clones. In a sandbox pod of its own it listens on the pod's
// address and takes a bearer token that only the runner holds.
package workspace

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/orchard-suite/graft/internal/shell"
)

// exitTrailer carries a command's exit code after its streamed output.
const exitTrailer = "Graft-Exit-Code"

// runRequest is one command for the workspace to run.
type runRequest struct {
	Dir  string   `json:"dir"`
	Env  []string `json:"env,omitempty"`
	Args []string `json:"args"`
}

// Server runs commands and moves files under Root.
type Server struct {
	Root string
	// Token, when set, is the bearer token every call but the health check
	// must carry. A workspace that listens beyond its pod's loopback, as a
	// sandbox pod's does, sets one: without it anything that can reach the
	// port can run code here.
	Token string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /run", s.guard(s.run))
	mux.HandleFunc("PUT /files", s.guard(s.put))
	mux.HandleFunc("GET /files", s.guard(s.get))
	mux.HandleFunc("DELETE /files", s.guard(s.remove))
	return mux
}

// guard refuses a call without the server's token, when it has one.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
				http.Error(w, "the workspace's token is required", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

// path resolves a path the runner named and refuses one outside Root.
func (s *Server) path(p string) (string, error) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside %s", p, root)
	}
	return abs, nil
}

// run streams the command's combined output, then its exit code as a trailer.
// A command that could not start answers 400 with the reason.
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Args) == 0 {
		http.Error(w, "want {dir, args}", http.StatusBadRequest)
		return
	}
	dir, err := s.path(req.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cmd := exec.CommandContext(r.Context(), req.Args[0], req.Args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), req.Env...)
	out := &flushWriter{w: w, ready: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = out, out
	w.Header().Set("Trailer", exitTrailer)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("%s: %v", req.Args[0], err), http.StatusBadRequest)
		return
	}
	out.start()
	code := 0
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			fmt.Fprintf(out, "\n%s: %v\n", req.Args[0], err)
		}
		code = cmd.ProcessState.ExitCode()
		if code == 0 {
			code = -1
		}
	}
	w.Header().Set(exitTrailer, strconv.Itoa(code))
}

func (s *Server) put(w http.ResponseWriter, r *http.Request) {
	p, err := s.path(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f, err := os.Create(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	p, err := s.path(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, f)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	p, err := s.path(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.RemoveAll(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// flushWriter sends each write to the client as it happens, so a long
// `make check` shows in the job log while it runs. A command's output waits
// for the answer's status: the command starts writing before the handler
// has said it started.
type flushWriter struct {
	w     http.ResponseWriter
	ready chan struct{}
}

// start sends the answer's status, and lets the output through.
func (f *flushWriter) start() {
	f.w.WriteHeader(http.StatusOK)
	close(f.ready)
}

func (f *flushWriter) Write(p []byte) (int, error) {
	<-f.ready
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// Client is the runner's side of the workspace.
type Client struct {
	URL  string
	HTTP *http.Client
	// Token is sent as the bearer token when the workspace asks for one.
	Token string
}

func (c *Client) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

func NewClient(baseURL string) *Client {
	return &Client{URL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{}}
}

// Run runs a command in the workspace the way shell.Run runs one here: a
// header line and the combined output go to log, and a non-zero exit is a
// *shell.Error.
func (c *Client) Run(ctx context.Context, log io.Writer, cmd shell.Cmd) (shell.Result, error) {
	fmt.Fprintf(log, "\n$ %s\n", strings.Join(cmd.Args, " "))
	if cmd.Stdin != "" {
		return shell.Result{}, errors.New("workspace: a command takes no stdin")
	}
	body, err := json.Marshal(runRequest{Dir: cmd.Dir, Env: cmd.Env, Args: cmd.Args})
	if err != nil {
		return shell.Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/run", bytes.NewReader(body))
	if err != nil {
		return shell.Result{}, err
	}
	c.auth(req)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return shell.Result{}, fmt.Errorf("workspace: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return shell.Result{}, fmt.Errorf("workspace: %s: %s", res.Status, strings.TrimSpace(string(msg)))
	}
	var buf bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(log, &buf), bufio.NewReader(res.Body)); err != nil {
		return shell.Result{Output: buf.String()}, fmt.Errorf("workspace: %w", err)
	}
	result := shell.Result{Output: buf.String()}
	code, err := strconv.Atoi(res.Trailer.Get(exitTrailer))
	if err != nil {
		return result, fmt.Errorf("workspace: %s ended without an exit code", cmd.Args[0])
	}
	result.ExitCode = code
	if code != 0 {
		return result, &shell.Error{Args: cmd.Args, Code: code, Tail: shell.Tail(result.Output, 2000)}
	}
	return result, nil
}

// Put writes r to path in the workspace.
func (c *Client) Put(ctx context.Context, path string, r io.Reader) error {
	return c.files(ctx, http.MethodPut, path, r, nil)
}

// Get copies the file at path in the workspace to w.
func (c *Client) Get(ctx context.Context, path string, w io.Writer) error {
	return c.files(ctx, http.MethodGet, path, nil, w)
}

// Remove deletes path in the workspace and everything under it.
func (c *Client) Remove(ctx context.Context, path string) error {
	return c.files(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) files(ctx context.Context, method, path string, body io.Reader, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, method, c.URL+"/files?path="+url.QueryEscape(path), body)
	if err != nil {
		return err
	}
	c.auth(req)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("workspace: %s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if _, err := io.Copy(out, res.Body); err != nil {
			return fmt.Errorf("workspace: %w", err)
		}
	}
	return nil
}
