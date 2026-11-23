package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/orchard-suite/graft/internal/desk"
	"github.com/orchard-suite/graft/internal/store"
)

// The agent's report. `graft report` writes the files Graft reads from
// beside the agent's clone, as Graft names them, and nothing else: it
// calls nothing and holds no credential. Graft reads the files as data,
// checks them, and writes the ticket itself.

const reportUsage = `usage:
  graft report step "<what you are doing>"
  graft report found "<what you found>"
  graft report test "<the test or command>" --passed|--failed ["<its result>"]
  graft report stuck "<what stops you, and what you tried>"
  graft report shot <file.png> --screen "<screen>" --before|--after ["<what it shows>"]
  graft report proof <criterion number> --shot <file.png> | --test "<test name>"
  graft report data "<what data the screens show>"
  graft report noshots "<why there are no screenshots>"
  graft report done "<what you built>"
  graft report left "<what is left, and why>"`

func reportCommand(args []string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New(reportUsage)
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	home, err := findHome(wd)
	if err != nil {
		return err
	}
	dir := filepath.Join(home, desk.ReportDir)
	switch args[0] {
	case "step", "found", "stuck":
		return addEntry(dir, desk.Entry{Kind: args[0], Text: strings.Join(args[1:], " ")}, out)
	case "test":
		e := desk.Entry{Kind: store.StepTest, Text: args[1]}
		var rest []string
		for _, a := range args[2:] {
			switch a {
			case "--passed", "--failed":
				ok := a == "--passed"
				e.OK = &ok
			default:
				rest = append(rest, a)
			}
		}
		if e.OK == nil {
			return errors.New(reportUsage)
		}
		e.Detail = strings.Join(rest, " ")
		return addEntry(dir, e, out)
	case "shot":
		return addShot(dir, args[1:], out)
	case "proof":
		return addProof(dir, args[1:], out)
	case "done", "left":
		text := strings.TrimSpace(strings.Join(args[1:], " "))
		if text == "" {
			return errors.New(reportUsage)
		}
		return editFinish(dir, func(f *desk.Finish) {
			if args[0] == "done" {
				f.Done, f.Summary = true, text
				return
			}
			f.Done = false
			f.Left = append(f.Left, text)
		}, out)
	case "data", "noshots":
		return editManifest(dir, func(m *desk.Manifest) error {
			if args[0] == "data" {
				m.Data = strings.Join(args[1:], " ")
			} else {
				m.None = strings.Join(args[1:], " ")
			}
			return nil
		}, out)
	}
	return errors.New(reportUsage)
}

// findHome is the request's home: the directory, at dir or above it, that
// holds the handle file.
func findHome(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, desk.HandleFile)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no handle here: this command works only inside a request Graft is working")
		}
		dir = parent
	}
}

// addEntry appends one line to the progress file.
func addEntry(dir string, e desk.Entry, out io.Writer) error {
	if strings.TrimSpace(e.Text) == "" {
		return errors.New(reportUsage)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, desk.ProgressFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintln(out, "reported")
	return nil
}

// addShot puts a screenshot in the screenshots folder, and names it in the
// manifest with its screen, its state, and what it shows.
func addShot(dir string, args []string, out io.Writer) error {
	var file, screen, state string
	var caption []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--screen" && i+1 < len(args):
			i++
			screen = args[i]
		case a == "--before" || a == "--after":
			state = strings.TrimPrefix(a, "--")
		case file == "":
			file = a
		default:
			caption = append(caption, a)
		}
	}
	if file == "" || screen == "" || state == "" {
		return errors.New(reportUsage)
	}
	shots := filepath.Join(dir, desk.ShotsDir)
	if err := os.MkdirAll(shots, 0o755); err != nil {
		return err
	}
	name := filepath.Base(file)
	if name == desk.ManifestFile {
		return fmt.Errorf("%s is the manifest's name; name the picture otherwise", name)
	}
	if dst := filepath.Join(shots, name); filepath.Clean(file) != dst {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return editManifest(dir, func(m *desk.Manifest) error {
		shot := desk.ManifestShot{File: name, Screen: screen, State: state, Caption: strings.Join(caption, " ")}
		for i := range m.Shots {
			if m.Shots[i].File == name {
				m.Shots[i] = shot
				return nil
			}
		}
		m.Shots = append(m.Shots, shot)
		return nil
	}, out)
}

// addProof says what proves one criterion of the plan.
func addProof(dir string, args []string, out io.Writer) error {
	if len(args) < 3 {
		return errors.New(reportUsage)
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return fmt.Errorf("%q is not a criterion's number in Done when", args[0])
	}
	return editManifest(dir, func(m *desk.Manifest) error {
		var p *store.Proof
		for i := range m.Proofs {
			if m.Proofs[i].Criterion == n {
				p = &m.Proofs[i]
			}
		}
		if p == nil {
			m.Proofs = append(m.Proofs, store.Proof{Criterion: n})
			p = &m.Proofs[len(m.Proofs)-1]
		}
		switch args[1] {
		case "--shot":
			p.Shots = append(p.Shots, filepath.Base(args[2]))
		case "--test":
			p.Test = strings.Join(args[2:], " ")
		default:
			return errors.New(reportUsage)
		}
		return nil
	}, out)
}

// editFinish reads the finish file, changes it, and writes it again. Once
// anything is left, the work is not finished, whatever was said before.
func editFinish(dir string, change func(f *desk.Finish), out io.Writer) error {
	p := filepath.Join(dir, desk.FinishFile)
	var f desk.Finish
	if raw, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(raw, &f); err != nil {
			return fmt.Errorf("%s is not a finish file: %w", p, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	change(&f)
	if len(f.Left) > 0 {
		f.Done = false
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(out, "reported")
	return nil
}

// editManifest reads the manifest, changes it, and writes it again.
func editManifest(dir string, change func(m *desk.Manifest) error, out io.Writer) error {
	p := filepath.Join(dir, desk.ShotsDir, desk.ManifestFile)
	var m desk.Manifest
	if raw, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s is not a manifest: %w", p, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := change(&m); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(out, "reported")
	return nil
}
