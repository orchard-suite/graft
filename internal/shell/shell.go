// Package shell runs commands for the pipeline, with their output copied to
// the job's log.
package shell

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Cmd describes one command. Env entries are added to the process
// environment; Stdin, when set, is fed to the process.
type Cmd struct {
	Dir   string
	Env   []string
	Stdin string
	Args  []string
}

// Result is what a finished command left behind.
type Result struct {
	Output   string
	ExitCode int
}

// Run executes the command, writing a header line and the combined output to
// log, and returns the output. A non-zero exit is an error that carries the
// code and the tail of the output.
func Run(ctx context.Context, log io.Writer, c Cmd) (Result, error) {
	fmt.Fprintf(log, "\n$ %s\n", strings.Join(c.Args, " "))
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin)
	}
	var buf bytes.Buffer
	w := io.MultiWriter(log, &buf)
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	res := Result{Output: buf.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if ok := asExit(err, &exitErr); ok {
			res.ExitCode = exitErr.ExitCode()
			return res, &Error{Args: c.Args, Code: res.ExitCode, Tail: Tail(res.Output, 2000)}
		}
		return res, fmt.Errorf("%s: %w", c.Args[0], err)
	}
	return res, nil
}

// Error is a command that exited non-zero.
type Error struct {
	Args []string
	Code int
	Tail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s exited %d:\n%s", strings.Join(e.Args, " "), e.Code, e.Tail)
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// Tail returns the last n bytes of s, cut at a line boundary.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
