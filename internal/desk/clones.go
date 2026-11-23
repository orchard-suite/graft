package desk

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// Each request has two clones. The agent's, under the work directory, is
// where the agent works and the gate runs; the runner reaches it only
// through the workspace. The runner's own, under the repo directory, is the
// only one the runner runs git in, and it pushes and lands from there, so
// no hook, git config, or Makefile the agent wrote ever runs beside the
// forge credential. Commits cross between the two as git bundles.

// agentRef is where the runner's clone keeps the agent's latest commits.
const agentRef = "refs/graft/agent"

// home is the request's directory in the workspace. It holds the agent's
// clone and, beside it and never inside it, the handle file.
func (d *Desk) home(r *store.Request) string {
	return filepath.Join(d.cfg.WorkDir, "requests", r.ID)
}

// dir is the agent's clone of the request's repository.
func (d *Desk) dir(r *store.Request) string { return filepath.Join(d.home(r), "repo") }

// repoDir is the runner's clone of the request's repository.
func (d *Desk) repoDir(r *store.Request) string {
	return filepath.Join(d.cfg.RepoDir, "requests", r.ID)
}

// wsBundle is the request's bundle in transit, as the workspace names it.
func (d *Desk) wsBundle(r *store.Request) string {
	return filepath.Join(d.cfg.WorkDir, ".bundles", r.ID+".bundle")
}

// localBundle is the request's bundle in transit, on the runner's side.
func (d *Desk) localBundle(r *store.Request) string {
	return filepath.Join(d.cfg.RepoDir, ".bundles", r.ID+".bundle")
}

// gitFlags go on every git the desk runs: Graft's identity, and no
// background maintenance, which would race the removal of a spent clone.
func (d *Desk) gitFlags() []string {
	return []string{"-c", "user.name=" + d.prog.Committer.Name, "-c", "user.email=" + d.prog.Committer.Email, "-c", "maintenance.auto=false"}
}

// committer is the environment that names Graft on a commit git writes.
func (d *Desk) committer() []string {
	return []string{"GIT_COMMITTER_NAME=" + d.prog.Committer.Name, "GIT_COMMITTER_EMAIL=" + d.prog.Committer.Email}
}

// git runs git in the runner's own clone.
func (d *Desk) git(ctx context.Context, dir string, log io.Writer, args ...string) (shell.Result, error) {
	full := append([]string{"git"}, d.gitFlags()...)
	full = append(full, args...)
	return shell.Run(ctx, log, shell.Cmd{Dir: dir, Args: full})
}

// wsGit runs git in the agent's clone, through the workspace.
func (d *Desk) wsGit(ctx context.Context, r *store.Request, dir string, log io.Writer, args ...string) (shell.Result, error) {
	full := append([]string{"git"}, d.gitFlags()...)
	full = append(full, args...)
	return d.box(r).WS.Run(ctx, log, shell.Cmd{Dir: dir, Args: full})
}

func hasGit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// ensureRepo gives the runner its clone of the repository, with origin's
// main fetched. A clone it has already is pointed at the forge again.
func (d *Desk) ensureRepo(ctx context.Context, r *store.Request, log io.Writer) error {
	dir, remote := d.repoDir(r), d.forge.CloneURL(r.Repo)
	if hasGit(dir) {
		if _, err := d.git(ctx, dir, log, "remote", "set-url", "origin", remote); err != nil {
			return err
		}
		_, err := d.git(ctx, dir, log, "fetch", "--quiet", "--tags", "origin")
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	_, err := d.git(ctx, "", log, "clone", "--quiet", remote, dir)
	return err
}

// rev is what a revision names in the runner's clone.
func (d *Desk) rev(ctx context.Context, r *store.Request, log io.Writer, rev string) (string, error) {
	res, err := d.git(ctx, d.repoDir(r), log, "rev-parse", rev)
	return strings.TrimSpace(res.Output), err
}

// seed gives the agent its clone in the session's box: the runner's main,
// the forge's main as origin/main, and the tags, with origin named as on
// the forge and Graft's identity for its commits. The runner's main is
// the forge's main for a new session, and the agent's own commits on top of
// it for a session that picks the work up again, so nothing depends on a
// clone that outlived its session. A repository that lands with
// release-damnit gets its commit-msg hook, so the agent's commits are
// checked as it makes them.
func (d *Desk) seed(ctx context.Context, r *store.Request, log io.Writer) error {
	dir := d.dir(r)
	if err := d.box(r).WS.Remove(ctx, dir); err != nil {
		return err
	}
	if err := d.send(ctx, r, log, "main", "refs/remotes/origin/main", "--tags"); err != nil {
		return err
	}
	defer d.dropBundle(ctx, r, log)
	if _, err := d.box(r).WS.Run(ctx, log, shell.Cmd{Dir: d.cfg.WorkDir, Args: []string{"mkdir", "-p", d.home(r)}}); err != nil {
		return err
	}
	if _, err := d.box(r).WS.Run(ctx, log, shell.Cmd{Dir: d.cfg.WorkDir, Args: []string{"git", "clone", "--quiet", "-b", "main", d.wsBundle(r), dir}}); err != nil {
		return err
	}
	if _, err := d.wsGit(ctx, r, dir, log, "remote", "set-url", "origin", d.forge.CloneURL(r.Repo)); err != nil {
		return err
	}
	if _, err := d.wsGit(ctx, r, dir, log, "fetch", "--quiet", d.wsBundle(r), "+refs/remotes/origin/main:refs/remotes/origin/main"); err != nil {
		return err
	}
	for _, kv := range [][2]string{{"user.name", d.prog.Committer.Name}, {"user.email", d.prog.Committer.Email}} {
		if _, err := d.wsGit(ctx, r, dir, log, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if landsWithReleaseDamnit(d.repoDir(r)) {
		if _, err := d.box(r).WS.Run(ctx, log, shell.Cmd{Dir: dir, Args: []string{"release-damnit", "hooks"}}); err != nil {
			return err
		}
	}
	return nil
}

// resume gives the agent a clone of the work so far: the runner's main,
// which holds the agent's commits or what landed, over the forge's main.
func (d *Desk) resume(ctx context.Context, r *store.Request, log io.Writer) error {
	if err := d.ensureRepo(ctx, r, log); err != nil {
		return err
	}
	return d.seed(ctx, r, log)
}

// fresh points the runner's clone at the forge's main as it is now and
// gives the agent a new clone of it. It returns main's commit.
func (d *Desk) fresh(ctx context.Context, r *store.Request, log io.Writer) (string, error) {
	if err := d.ensureRepo(ctx, r, log); err != nil {
		return "", err
	}
	if _, err := d.git(ctx, d.repoDir(r), log, "checkout", "--quiet", "-B", "main", "refs/remotes/origin/main"); err != nil {
		return "", err
	}
	if _, err := d.git(ctx, d.repoDir(r), log, "update-ref", "-d", agentRef); err != nil {
		return "", err
	}
	head, err := d.rev(ctx, r, log, "HEAD")
	if err != nil {
		return "", err
	}
	return head, d.seed(ctx, r, log)
}

// send bundles revs from the runner's clone and puts the bundle in the
// workspace at wsBundle.
func (d *Desk) send(ctx context.Context, r *store.Request, log io.Writer, revs ...string) error {
	local := d.localBundle(r)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	defer os.Remove(local)
	if _, err := d.git(ctx, d.repoDir(r), log, append([]string{"bundle", "create", "-q", local}, revs...)...); err != nil {
		return err
	}
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	return d.box(r).WS.Put(ctx, d.wsBundle(r), f)
}

func (d *Desk) dropBundle(ctx context.Context, r *store.Request, log io.Writer) {
	if err := d.box(r).WS.Remove(ctx, d.wsBundle(r)); err != nil {
		fmt.Fprintf(log, "%v\n", err)
	}
}

// takeCommits brings the agent's commits since its origin/main, up to head,
// into the runner's clone as agentRef and checks them out on main there.
// The bundle is data: fetching it runs nothing the agent wrote. The
// runner's clone is Graft's own and never the forge's main: work kept
// there lands only when its build is finished.
func (d *Desk) takeCommits(ctx context.Context, r *store.Request, log io.Writer, head string) error {
	if _, err := d.wsGit(ctx, r, d.dir(r), log, "branch", "-f", "graft-take", head); err != nil {
		return err
	}
	if _, err := d.wsGit(ctx, r, d.dir(r), log, "bundle", "create", "-q", d.wsBundle(r), "refs/remotes/origin/main..graft-take"); err != nil {
		return err
	}
	defer d.dropBundle(ctx, r, log)
	local := d.localBundle(r)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	defer os.Remove(local)
	f, err := os.Create(local)
	if err != nil {
		return err
	}
	err = d.box(r).WS.Get(ctx, d.wsBundle(r), f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	dir := d.repoDir(r)
	if _, err := d.git(ctx, dir, log, "fetch", "--quiet", local, "+refs/heads/graft-take:"+agentRef); err != nil {
		return err
	}
	_, err = d.git(ctx, dir, log, "checkout", "--quiet", "-B", "main", agentRef)
	return err
}

// landsWithReleaseDamnit says whether the repository versions and lands
// with release-damnit, which reads release-please-config.json.
func landsWithReleaseDamnit(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "release-please-config.json"))
	return err == nil
}

// rebasing says whether the agent's clone is in the middle of a rebase.
func (d *Desk) rebasing(ctx context.Context, r *store.Request, dir string) bool {
	_, err := d.box(r).WS.Run(ctx, io.Discard, shell.Cmd{Dir: dir, Args: []string{"test", "-d", ".git/rebase-merge", "-o", "-d", ".git/rebase-apply"}})
	return err == nil
}

// hasMakeTarget says whether the Makefile at the root of a clone in the
// workspace defines target.
func (d *Desk) hasMakeTarget(ctx context.Context, r *store.Request, dir, target string) bool {
	_, err := d.box(r).WS.Run(ctx, io.Discard, shell.Cmd{Dir: dir, Args: []string{"sh", "-c", "test -f Makefile && make -n " + target}})
	return err == nil
}
