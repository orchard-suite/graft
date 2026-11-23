// Package ion is the Ion adapter of the agent runtime crossing. It drives
// the Ion engine's CLI for headless runs. The engine is found through its
// own environment (ION_DATA_DIR, ION_SOCKET_PATH).
package ion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/runtime"
)

type Client struct {
	Binary string
}

func New() *Client { return &Client{Binary: "ion"} }

func (c *Client) Name() string { return "ion" }

// args is the CLI's arguments for one session's prompt, read from stdin.
func args(s runtime.Session) []string {
	a := []string{"prompt", "--attach", "--model", s.Model, "--timeout", s.Timeout.String(), "-"}
	if s.Key != "" {
		a = append(a, "--key", s.Key)
	}
	if s.MaxTurns > 0 {
		a = append(a, "--max-turns", strconv.Itoa(s.MaxTurns))
	}
	if s.Budget > 0 {
		a = append(a, "--max-budget", strconv.FormatFloat(s.Budget, 'f', 2, 64))
	}
	return a
}

func (c *Client) Run(ctx context.Context, s runtime.Session, out io.Writer) (runtime.Usage, error) {
	if err := engineUp(); err != nil {
		return runtime.Usage{}, err
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, c.Binary, args(s)...)
	cmd.Dir = s.Dir
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader(s.Prompt)
	tail := &tailWriter{}
	w := io.MultiWriter(out, tail)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	use := usage(tail.String(), time.Since(start))
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return use, ended(exitErr.ExitCode(), tail.String())
	}
	return use, err
}

// budgetLine is how the ion CLI says the engine stopped a run at its spend
// limit: it exits 1 and writes this on its last lines.
const budgetLine = "Error: budget exceeded:"

// ended reads how a prompt that exited non-zero ended, from its exit code
// and the last of its output: a timeout, the spend limit, or a failure.
func ended(code int, tail string) error {
	switch {
	case code == 124:
		return runtime.ErrTimeout
	case code == 1 && strings.Contains(tail, budgetLine):
		return runtime.ErrBudget
	}
	return fmt.Errorf("ion prompt exited %d", code)
}

// tailWriter keeps the last few kilobytes written to it.
type tailWriter struct{ b []byte }

const tailMax = 4 << 10

func (t *tailWriter) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > tailMax {
		t.b = append(t.b[:0], t.b[len(t.b)-tailMax:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.b) }

// engineUp checks that the engine answers at ION_SOCKET_PATH. The ion CLI
// starts an engine of its own when it finds none, and one started here would
// run the agent beside the runner's credentials, so the runner refuses
// instead. Without ION_SOCKET_PATH, as on a laptop, the CLI's own default
// stands.
func engineUp() error {
	addr := os.Getenv("ION_SOCKET_PATH")
	if addr == "" {
		return nil
	}
	network := "unix"
	if !strings.Contains(addr, "/") && strings.Contains(addr, ":") {
		network = "tcp"
	}
	conn, err := net.DialTimeout(network, addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("ion: no engine at %s: %w", addr, err)
	}
	return conn.Close()
}

func (c *Client) Ask(ctx context.Context, s runtime.Session) (string, runtime.Usage, error) {
	var b strings.Builder
	use, err := c.Run(ctx, s, &b)
	return answer(b.String()), use, err
}

// Stop ends a keyed session. Safe to call for a session that is gone.
func (c *Client) Stop(ctx context.Context, key string) error {
	cmd := exec.CommandContext(ctx, c.Binary, "stop", "--key", key)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "not found") {
		return fmt.Errorf("ion stop: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
