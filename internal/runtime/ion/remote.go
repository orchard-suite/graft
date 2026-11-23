package ion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/shell"
)

// Commands is somewhere else that runs commands and holds files: a
// sandbox's workspace.
type Commands interface {
	Run(ctx context.Context, log io.Writer, cmd shell.Cmd) (shell.Result, error)
	Put(ctx context.Context, path string, r io.Reader) error
	Remove(ctx context.Context, path string) error
}

// Remote drives the Ion CLI where the engine is, through a sandbox's
// workspace, instead of from here. The prompt crosses as a file.
type Remote struct {
	In Commands
	// Scratch is a directory in the sandbox where prompt files go.
	Scratch string
	Binary  string
	n       atomic.Int64
}

func (c *Remote) Name() string { return "ion" }

func (c *Remote) binary() string {
	if c.Binary != "" {
		return c.Binary
	}
	return "ion"
}

func (c *Remote) Run(ctx context.Context, s runtime.Session, out io.Writer) (runtime.Usage, error) {
	file := path.Join(c.Scratch, fmt.Sprintf(".prompt-%d", c.n.Add(1)))
	if err := c.In.Put(ctx, file, strings.NewReader(s.Prompt)); err != nil {
		return runtime.Usage{}, err
	}
	defer c.In.Remove(context.WithoutCancel(ctx), file)
	start := time.Now()
	// $0 is the prompt file; the rest is the command, fed the file.
	argv := append([]string{"sh", "-c", `exec "$@" < "$0"`, file, c.binary()}, args(s)...)
	res, err := c.In.Run(ctx, out, shell.Cmd{Dir: s.Dir, Args: argv})
	use := usage(shell.Tail(res.Output, tailMax), time.Since(start))
	var exit *shell.Error
	if errors.As(err, &exit) {
		return use, ended(exit.Code, exit.Tail)
	}
	return use, err
}

func (c *Remote) Ask(ctx context.Context, s runtime.Session) (string, runtime.Usage, error) {
	var b strings.Builder
	use, err := c.Run(ctx, s, &b)
	return answer(b.String()), use, err
}

func (c *Remote) Stop(ctx context.Context, key string) error {
	res, err := c.In.Run(ctx, io.Discard, shell.Cmd{Dir: c.Scratch, Args: []string{c.binary(), "stop", "--key", key}})
	if err != nil && !strings.Contains(res.Output, "not found") {
		return fmt.Errorf("ion stop: %w", err)
	}
	return nil
}
