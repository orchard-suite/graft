package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/desk"
)

// The agent's side of the handle. An agent in the sandbox runs `graft
// tool` and `graft evidence`; they find the handle file beside the clone
// and call Graft's loopback port with its secret. The CLI holds no key:
// Graft asks the hub, as the person who asked.

// findHandle reads the handle file in dir or the nearest directory above it.
func findHandle(dir string) (desk.Handle, error) {
	var h desk.Handle
	for {
		raw, err := os.ReadFile(filepath.Join(dir, desk.HandleFile))
		if err == nil {
			if err := json.Unmarshal(raw, &h); err != nil || h.URL == "" || h.Secret == "" {
				return h, errors.New("the handle file is not a handle")
			}
			return h, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return h, errors.New("no handle here: this command works only inside a request Graft is working")
		}
		dir = parent
	}
}

// callHandle sends one call to Graft's handle port and decodes its answer.
func callHandle(method, path string, in, out any) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	h, err := findHandle(wd)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, strings.TrimRight(h.URL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.Secret)
	req.Header.Set("Content-Type", "application/json")
	// A tool may run for minutes on its runner.
	res, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("Graft did not answer: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("Graft answered %s", res.Status)
	}
	return json.Unmarshal(raw, out)
}

const toolUsage = `usage:
  graft tool list
  graft tool call <id> [--env production|nonproduction] [name=value ...]`

func toolCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(toolUsage)
	}
	switch args[0] {
	case "list":
		var list desk.ToolList
		if err := callHandle(http.MethodGet, "/tools", nil, &list); err != nil {
			return err
		}
		if len(list.Tools) == 0 {
			fmt.Fprintln(out, "This repository declares no tools.")
			return nil
		}
		fmt.Fprintf(out, "Tools this repository declares. A call goes to %s unless --env says otherwise.\n\n", list.Default)
		for _, t := range list.Tools {
			fmt.Fprintf(out, "%s  (tier %s; %s)\n", t.ID, t.Tier, strings.Join(t.Environments, ", "))
			if t.Title != "" || t.Description != "" {
				fmt.Fprintf(out, "    %s\n", strings.TrimSpace(t.Title+" "+t.Description))
			}
			for name, def := range t.Inputs {
				fmt.Fprintf(out, "    input %s: %v\n", name, def["type"])
			}
		}
		return nil
	case "call":
		id, env, inputs, err := parseCall(args[1:])
		if err != nil {
			return err
		}
		var res desk.ToolResult
		if err := callHandle(http.MethodPost, "/tools/"+url.PathEscape(id)+"/call", map[string]any{"env": env, "inputs": inputs}, &res); err != nil {
			return err
		}
		fmt.Fprintf(out, "evidence %d (%s)\n", res.Evidence, res.State)
		if res.Truncated {
			fmt.Fprintln(out, "The answer was cut; this is its head.")
		}
		fmt.Fprintln(out, strings.TrimRight(res.Text, "\n"))
		return nil
	}
	return errors.New(toolUsage)
}

// parseCall reads `<id> [--env <env>] [name=value ...]`.
func parseCall(args []string) (id, env string, inputs map[string]any, err error) {
	inputs = map[string]any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--env":
			if i+1 == len(args) {
				return "", "", nil, errors.New(toolUsage)
			}
			i++
			env = args[i]
		case strings.HasPrefix(a, "--env="):
			env = strings.TrimPrefix(a, "--env=")
		case id == "" && !strings.Contains(a, "="):
			id = a
		default:
			name, value, ok := strings.Cut(a, "=")
			if !ok || name == "" {
				return "", "", nil, errors.New(toolUsage)
			}
			inputs[name] = value
		}
	}
	if id == "" {
		return "", "", nil, errors.New(toolUsage)
	}
	return id, env, inputs, nil
}

const evidenceUsage = `usage:
  graft evidence add <file:line or command> "<what it shows>"`

func evidenceCommand(args []string, out io.Writer) error {
	if len(args) < 3 || args[0] != "add" {
		return errors.New(evidenceUsage)
	}
	var res struct {
		Evidence int `json:"evidence"`
	}
	if err := callHandle(http.MethodPost, "/evidence", map[string]string{"ref": args[1], "note": strings.Join(args[2:], " ")}, &res); err != nil {
		return err
	}
	fmt.Fprintf(out, "evidence %d\n", res.Evidence)
	return nil
}
