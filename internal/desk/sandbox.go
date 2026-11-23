package desk

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// A box is the sandbox of one agent session: the screening call, one
// investigation, or one build with its rounds. It holds the agent's clone,
// runs the agent, and runs every command that reads what the agent wrote.
// It holds no forge credential and no key that changes anything. A box is
// opened when its session starts and shut when the session ends; nothing
// in it is needed afterward, because the agent's commits cross to the
// runner's clone as a bundle before it shuts.

// Box is one open sandbox.
type Box struct {
	WS Workspace
	RT runtime.Runtime
	// Close shuts the sandbox and drops everything in it.
	Close func(ctx context.Context)
}

// BoxFor names the session a box is for.
type BoxFor struct {
	Request string
	// Stage is screen, investigate, or build.
	Stage string
	// Key is the agent session's key.
	Key string
	// Home is the request's directory inside the box.
	Home string
}

// Sandboxes opens boxes.
type Sandboxes interface {
	Open(ctx context.Context, f BoxFor) (*Box, error)
}

// enter opens the box for a session of the request. A box left open by a
// stage that was cut short is shut first.
func (d *Desk) enter(ctx context.Context, r *store.Request, stage string) error {
	d.shut(ctx, r)
	box, err := d.boxes.Open(ctx, BoxFor{Request: r.ID, Stage: stage, Key: d.key(r, stage), Home: d.home(r)})
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.open[r.ID] = box
	d.mu.Unlock()
	return nil
}

// shut closes the request's box, if one is open.
func (d *Desk) shut(ctx context.Context, r *store.Request) {
	d.mu.Lock()
	box := d.open[r.ID]
	delete(d.open, r.ID)
	d.mu.Unlock()
	if box != nil {
		box.Close(context.WithoutCancel(ctx))
	}
}

// box is the request's open box. With none open, every call on it fails:
// a stage that needs a sandbox must open one.
func (d *Desk) box(r *store.Request) *Box {
	d.mu.Lock()
	defer d.mu.Unlock()
	if box := d.open[r.ID]; box != nil {
		return box
	}
	return &Box{WS: noWorkspace{}, RT: noRuntime{}, Close: func(context.Context) {}}
}

var errNoBox = errors.New("no sandbox is open for this request")

// noWorkspace and noRuntime answer every call with errNoBox.
type noWorkspace struct{}

func (noWorkspace) Run(context.Context, io.Writer, shell.Cmd) (shell.Result, error) {
	return shell.Result{}, errNoBox
}
func (noWorkspace) Put(context.Context, string, io.Reader) error { return errNoBox }
func (noWorkspace) Get(context.Context, string, io.Writer) error { return errNoBox }
func (noWorkspace) Remove(context.Context, string) error         { return errNoBox }

type noRuntime struct{}

func (noRuntime) Name() string                       { return "none" }
func (noRuntime) Stop(context.Context, string) error { return nil }
func (noRuntime) Run(context.Context, runtime.Session, io.Writer) (runtime.Usage, error) {
	return runtime.Usage{}, errNoBox
}
func (noRuntime) Ask(context.Context, runtime.Session) (string, runtime.Usage, error) {
	return "", runtime.Usage{}, errNoBox
}

// Shared is the sandbox of a Graft that runs its engine and its workspace
// beside it in one pod: every session uses the same two, each in its own
// directory. It fits an estate where everyone who may ask is trusted with
// everything any request reads. Shutting a box stops its agent session and
// removes its directory.
type Shared struct {
	Workspace Workspace
	Runtime   runtime.Runtime
}

func (s *Shared) Open(_ context.Context, f BoxFor) (*Box, error) {
	return &Box{WS: s.Workspace, RT: localDirs{s.Runtime}, Close: func(ctx context.Context) {
		_ = s.Runtime.Stop(ctx, f.Key)
		_ = s.Workspace.Remove(ctx, f.Home)
	}}, nil
}

// localDirs makes a session's directory exist on the runner's side before
// the agent runs: a runtime driven from here names a session's directory
// by its own working directory. The directory stays empty here; the clone
// is the workspace's.
type localDirs struct{ runtime.Runtime }

func (l localDirs) Run(ctx context.Context, s runtime.Session, out io.Writer) (runtime.Usage, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return runtime.Usage{}, err
	}
	return l.Runtime.Run(ctx, s, out)
}

func (l localDirs) Ask(ctx context.Context, s runtime.Session) (string, runtime.Usage, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", runtime.Usage{}, err
	}
	return l.Runtime.Ask(ctx, s)
}
