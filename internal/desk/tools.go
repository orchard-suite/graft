package desk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	kit "github.com/orchard-suite/platformkit/v3/annals"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/hub"
	"github.com/orchard-suite/graft/internal/program"
	"github.com/orchard-suite/graft/internal/store"
)

// mirror is the runner's bare clone of a repository's main branch, where
// the repository file is read from. Nothing a request's agent wrote is
// ever in it.
func (d *Desk) mirror(repo *program.Repository) string {
	return filepath.Join(d.cfg.RepoDir, "main", strings.ReplaceAll(repo.Path, "/", "__")+".git")
}

// readRepoFile fetches the repository's main branch and reads graft.yaml
// from it. A repository without the file declares nothing.
func (d *Desk) readRepoFile(ctx context.Context, repo *program.Repository) (*program.RepoFile, error) {
	dir, remote := d.mirror(repo), d.forge.CloneURL(repo.Path)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return nil, err
		}
		if _, err := d.git(ctx, "", io.Discard, "init", "--quiet", "--bare", dir); err != nil {
			return nil, err
		}
	}
	if _, err := d.git(ctx, dir, io.Discard, "fetch", "--quiet", "--depth", "1", remote, "+refs/heads/main:refs/heads/main"); err != nil {
		return nil, err
	}
	if _, err := d.git(ctx, dir, io.Discard, "cat-file", "-e", "refs/heads/main:"+program.RepoFileName); err != nil {
		return &program.RepoFile{}, nil
	}
	res, err := d.git(ctx, dir, io.Discard, "show", "refs/heads/main:"+program.RepoFileName)
	if err != nil {
		return nil, err
	}
	return d.prog.ParseRepoFile([]byte(res.Output))
}

// SyncTools reads the repository file from main and makes the hub's tool
// actions match it. A file that does not parse changes nothing: the tools
// declared before stay as they were.
func (d *Desk) SyncTools(ctx context.Context, repo *program.Repository) error {
	file, err := d.readRepoFile(ctx, repo)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", repo.Path, program.RepoFileName, err)
	}
	was, err := d.store.DeclaredTools(ctx, repo.Path)
	if err != nil {
		return err
	}
	now, err := d.hub.SyncTools(ctx, repo, file, was)
	if err != nil {
		return err
	}
	if err := d.store.SetDeclaredTools(ctx, repo.Path, now); err != nil {
		return err
	}
	d.mu.Lock()
	d.files[repo.Path] = file
	d.mu.Unlock()
	return nil
}

// SyncAllTools syncs every repository of the program and logs each one
// that could not be.
func (d *Desk) SyncAllTools(ctx context.Context) {
	for i := range d.prog.Repositories {
		repo := &d.prog.Repositories[i]
		if err := d.SyncTools(ctx, repo); err != nil && ctx.Err() == nil {
			d.log.Warn("tools not synced", "repo", repo.Path, "error", err)
		}
	}
}

// repoFile is the repository file last read from main, read now when it
// has not been yet. It is never nil.
func (d *Desk) repoFile(ctx context.Context, repo *program.Repository) *program.RepoFile {
	d.mu.Lock()
	file := d.files[repo.Path]
	d.mu.Unlock()
	if file != nil {
		return file
	}
	if err := d.SyncTools(ctx, repo); err != nil {
		d.log.Warn("tools not synced", "repo", repo.Path, "error", err)
		return &program.RepoFile{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.files[repo.Path]
}

// ErrNoHandle is a handle that names no request an agent is working on.
var ErrNoHandle = errors.New("no request is being worked under this handle")

// ErrNoTool is a tool the repository does not declare, or not for that
// environment.
var ErrNoTool = errors.New("the repository declares no such tool")

// ByHandle is the request an agent's handle secret serves. A handle works
// only while an agent is on the request.
func (d *Desk) ByHandle(ctx context.Context, secret string) (*store.Request, error) {
	if secret == "" {
		return nil, ErrNoHandle
	}
	r, err := d.store.ByHandle(ctx, HashSecret(secret))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoHandle
	}
	if err != nil {
		return nil, err
	}
	if r.State != store.StateInvestigating && r.State != store.StateBuilding {
		return nil, ErrNoHandle
	}
	return r, nil
}

// ToolInfo is one declared tool, as the agent's CLI lists it.
type ToolInfo struct {
	ID          string                    `json:"id"`
	Title       string                    `json:"title,omitempty"`
	Description string                    `json:"description,omitempty"`
	Tier        string                    `json:"tier"`
	Inputs      map[string]map[string]any `json:"inputs,omitempty"`
	// Environments are the tiers the tool can be called in.
	Environments []string `json:"environments"`
}

// ToolList is what `graft tool list` shows.
type ToolList struct {
	// Default is the environment a call goes to when it names none.
	Default string     `json:"default"`
	Tools   []ToolInfo `json:"tools"`
}

func (d *Desk) defaultEnv(r *store.Request) string {
	if r.Environment == program.Production {
		return program.Production
	}
	return program.NonProduction
}

// Tools lists the tools the request's repository declares on main. Whether
// the request holds a tool's tier is the hub's to say, on the call.
func (d *Desk) Tools(ctx context.Context, r *store.Request) (ToolList, error) {
	repo, err := d.repo(r)
	if err != nil {
		return ToolList{}, err
	}
	list := ToolList{Default: d.defaultEnv(r), Tools: []ToolInfo{}}
	for _, t := range d.repoFile(ctx, repo).Tools {
		info := ToolInfo{ID: t.ID, Title: t.Title, Description: t.Description, Tier: t.Tier, Inputs: t.Inputs}
		for _, env := range []string{program.Production, program.NonProduction} {
			if t.Action != "" || d.prog.Runners[t.RunsAs].For(env) != "" {
				info.Environments = append(info.Environments, env)
			}
		}
		list.Tools = append(list.Tools, info)
	}
	return list, nil
}

// ToolResult is what one tool call came to.
type ToolResult struct {
	// Evidence is the number the answer was kept under.
	Evidence  int    `json:"evidence"`
	State     string `json:"state"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// CallTool calls one declared tool through the hub's door, lent to the
// person who asked, and keeps the answer as numbered evidence. The hub
// decides whether the request holds the tier; Graft only names the tool
// the repository declared. Annals gets the shape of the call, never
// its values.
func (d *Desk) CallTool(ctx context.Context, r *store.Request, id, env string, inputs map[string]any) (*ToolResult, error) {
	repo, err := d.repo(r)
	if err != nil {
		return nil, err
	}
	if env == "" {
		env = d.defaultEnv(r)
	}
	if env != program.Production && env != program.NonProduction {
		return nil, fmt.Errorf("%w: environment is %s or %s", ErrNoTool, program.Production, program.NonProduction)
	}
	var tool *program.Tool
	for i, t := range d.repoFile(ctx, repo).Tools {
		if t.ID == id {
			tool = &d.repoFile(ctx, repo).Tools[i]
		}
	}
	if tool == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoTool, id)
	}
	action := tool.Action
	if action == "" {
		if d.prog.Runners[tool.RunsAs].For(env) == "" {
			return nil, fmt.Errorf("%w: %s has no runner for %s", ErrNoTool, id, env)
		}
		action = d.prog.ToolAction(repo, id, env)
	}
	lend := hub.Lend{Person: r.Person, Lane: r.Lane, Entity: repo.Entity}
	// A build under an approved plan holds what that plan's run was granted.
	if r.State == store.StateBuilding && r.Run != "" {
		lend.Run = r.Run
	}
	step := annals.Step{Verb: "request.tool_called", Items: []kit.Item{{System: "orrery", Kind: "tool", ID: action, Access: tool.Tier}},
		Labels: map[string]string{"tool": id, "tier": tool.Tier, "tool_environment": env, "stage": r.State}}
	answer, err := d.hub.CallTool(ctx, action, inputs, lend)
	if err != nil {
		why := hub.Refused(err)
		if why == "" {
			return nil, err
		}
		step.Outcome, step.Reason = annals.Refused, why
		n, _ := d.store.AddEvidence(ctx, r.ID, "tool", fmt.Sprintf("%s (%s): refused", id, env), why)
		d.annals.Post(ctx, r, fmt.Sprintf("e%d", n), step)
		return &ToolResult{Evidence: n, State: "refused", Text: why}, nil
	}
	if env == program.Production {
		if err := d.store.MarkReadProduction(ctx, r.ID); err != nil {
			return nil, err
		}
	}
	title := fmt.Sprintf("%s (%s): %s", id, env, answer.State)
	n, err := d.store.AddEvidence(ctx, r.ID, "tool", title, answer.Text)
	if err != nil {
		return nil, err
	}
	if answer.State != "succeeded" {
		step.Outcome, step.Reason = annals.Failed, "the tool's run ended "+answer.State
	}
	d.annals.Post(ctx, r, fmt.Sprintf("e%d", n), step)
	return &ToolResult{Evidence: n, State: answer.State, Text: answer.Text, Truncated: answer.Truncated}, nil
}

// AddEvidence keeps something the agent read in the code or saw in a run,
// and returns its number.
func (d *Desk) AddEvidence(ctx context.Context, r *store.Request, ref, note string) (int, error) {
	ref, note = strings.TrimSpace(ref), strings.TrimSpace(note)
	if ref == "" || note == "" {
		return 0, errors.New("evidence needs where it is and what it shows")
	}
	return d.store.AddEvidence(ctx, r.ID, "code", clip(ref, 300), clip(note, 8000))
}
