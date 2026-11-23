package workspace

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orchard-suite/graft/internal/shell"
)

func newClient(t *testing.T) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	srv := httptest.NewServer((&Server{Root: root}).Handler())
	t.Cleanup(srv.Close)
	return NewClient(srv.URL), root
}

func TestRunStreamsOutputAndExitCode(t *testing.T) {
	c, root := newClient(t)
	ctx := context.Background()
	var log bytes.Buffer
	res, err := c.Run(ctx, &log, shell.Cmd{Dir: root, Env: []string{"WHO=workspace"}, Args: []string{"sh", "-c", "echo hello $WHO; pwd"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "hello workspace") || !strings.Contains(res.Output, root) {
		t.Fatalf("output %q", res.Output)
	}
	if !strings.Contains(log.String(), "$ sh -c") || !strings.Contains(log.String(), "hello workspace") {
		t.Fatalf("the log has the command and its output: %q", log.String())
	}

	res, err = c.Run(ctx, &log, shell.Cmd{Dir: root, Args: []string{"sh", "-c", "echo red; exit 3"}})
	var shErr *shell.Error
	if !errors.As(err, &shErr) || shErr.Code != 3 || res.ExitCode != 3 || !strings.Contains(shErr.Tail, "red") {
		t.Fatalf("a non-zero exit is a shell.Error with its code and output: %v, %+v", err, res)
	}

	if _, err := c.Run(ctx, &log, shell.Cmd{Dir: root, Args: []string{"no-such-command-here"}}); err == nil || errors.As(err, &shErr) {
		t.Fatalf("a command that cannot start is an error of its own: %v", err)
	}
}

func TestPathsStayUnderTheRoot(t *testing.T) {
	c, root := newClient(t)
	ctx := context.Background()
	outside := filepath.Dir(root)
	if _, err := c.Run(ctx, &bytes.Buffer{}, shell.Cmd{Dir: outside, Args: []string{"true"}}); err == nil {
		t.Fatal("a command outside the root runs")
	}
	if err := c.Put(ctx, filepath.Join(root, "..", "escape"), strings.NewReader("x")); err == nil {
		t.Fatal("a file outside the root is written")
	}
	if err := c.Remove(ctx, outside); err == nil {
		t.Fatal("a directory outside the root is removed")
	}
}

func TestFilesRoundTrip(t *testing.T) {
	c, root := newClient(t)
	ctx := context.Background()
	path := filepath.Join(root, "a", "b.bundle")
	if err := c.Put(ctx, path, strings.NewReader("bytes")); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := c.Get(ctx, path, &got); err != nil || got.String() != "bytes" {
		t.Fatalf("get %q, %v", got.String(), err)
	}
	if err := c.Remove(ctx, filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("removed file still there: %v", err)
	}
	if err := c.Get(ctx, path, &got); err == nil {
		t.Fatal("a missing file is an error")
	}
}
