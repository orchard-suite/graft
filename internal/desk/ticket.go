package desk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// A request's commits name its ticket, so whoever reads main finds the
// request, and the revert tells the request's commits apart (ADR-5005).
// Graft names it itself, as a trailer under each message, and never
// touches a subject: a repository's subject rules stay the builder's to
// meet, and no model is trusted to remember a reference.

// ticketTrailer is the trailer Graft adds to a commit of ticket n.
func ticketTrailer(n int) string { return "Refs: #" + strconv.Itoa(n) }

// namesTicket says whether a commit message names ticket n, anywhere in it.
// The gate and the revert both read a message by it.
func namesTicket(msg string, n int) bool {
	return regexp.MustCompile(`#` + strconv.Itoa(n) + `\b`).MatchString(msg)
}

// commitMessage is where, beside the builder's clone and never inside it,
// a message waits while Graft writes its commit again.
const commitMessage = "commit-message"

// commit is one commit as nameTicket reads it.
type commit struct {
	sha, msg                  string
	parents                   []string
	author, email, authoredAt string
}

// authorEnv keeps a commit's author and date on the commit written for it.
func (c commit) authorEnv() []string {
	return []string{"GIT_AUTHOR_NAME=" + c.author, "GIT_AUTHOR_EMAIL=" + c.email, "GIT_AUTHOR_DATE=" + c.authoredAt}
}

// readCommits reads the commits of rng in the agent's clone, parents
// before children.
func (d *Desk) readCommits(ctx context.Context, r *store.Request, dir, rng string, log io.Writer) ([]commit, error) {
	res, err := d.wsGit(ctx, r, dir, log, "log", "--topo-order", "--reverse", "--date=raw", "--format=%H%x00%P%x00%an%x00%ae%x00%ad%x00%B%x01", rng)
	if err != nil {
		return nil, err
	}
	var out []commit
	for _, entry := range strings.Split(res.Output, "\x01") {
		f := strings.SplitN(strings.TrimLeft(entry, "\n"), "\x00", 6)
		if len(f) != 6 {
			continue
		}
		out = append(out, commit{sha: f[0], parents: strings.Fields(f[1]), author: f[2], email: f[3], authoredAt: f[4], msg: f[5]})
	}
	return out, nil
}

// nameTicket gives every commit of rng that does not name the request's
// ticket its trailer, and moves HEAD to the commits written for them. It
// writes only the builder's own commits above the gate's base, which no
// forge holds yet. Each new commit keeps its tree, its parents, its author,
// and its date; Graft is its committer, as for a rebase. A commit after
// one written again is written again on top of it, with its message as it
// was. The repository's commit-msg hook judges each new message, as it
// judged the builder's: what it refuses is the gate's problem, returned with
// nothing written.
func (d *Desk) nameTicket(ctx context.Context, r *store.Request, dir, rng string, log io.Writer) (string, error) {
	n := r.Ticket.Number
	commits, err := d.readCommits(ctx, r, dir, rng, log)
	if err != nil {
		return "", err
	}
	lacking := 0
	for _, c := range commits {
		if !namesTicket(c.msg, n) {
			lacking++
		}
	}
	if lacking == 0 {
		return "", nil
	}
	file := filepath.Join(d.home(r), commitMessage)
	defer func() {
		if err := d.box(r).WS.Remove(context.WithoutCancel(ctx), file); err != nil {
			fmt.Fprintf(log, "%v\n", err)
		}
	}()
	written := map[string]string{}
	for _, c := range commits {
		parents, moved := make([]string, len(c.parents)), false
		for i, p := range c.parents {
			parents[i] = p
			if np, ok := written[p]; ok {
				parents[i], moved = np, true
			}
		}
		lacks := !namesTicket(c.msg, n)
		if !lacks && !moved {
			continue
		}
		if err := d.box(r).WS.Put(ctx, file, strings.NewReader(c.msg)); err != nil {
			return "", err
		}
		if lacks {
			if _, err := d.wsGit(ctx, r, dir, log, "interpret-trailers", "--in-place", "--trailer", ticketTrailer(n), file); err != nil {
				return "", err
			}
			if _, err := d.wsGit(ctx, r, dir, log, "hook", "run", "--ignore-missing", "commit-msg", "--", file); err != nil {
				var refused *shell.Error
				if !errors.As(err, &refused) {
					return "", err
				}
				return fmt.Sprintf("Graft names the ticket under each commit message as `%s`, and the repository's commit-msg hook refuses that for %s %s. Name the ticket as `#%d` in that message yourself, in a form the hook accepts:\n%s",
					ticketTrailer(n), shortSHA(c.sha), firstLine(c.msg), n, refused.Tail), nil
			}
		}
		args := append(append([]string{"git"}, d.gitFlags()...), "commit-tree", c.sha+"^{tree}", "-F", file)
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		res, err := d.box(r).WS.Run(ctx, log, shell.Cmd{Dir: dir, Env: c.authorEnv(), Args: args})
		if err != nil {
			return "", err
		}
		written[c.sha] = strings.TrimSpace(res.Output)
	}
	head, err := d.wsGit(ctx, r, dir, log, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	old := strings.TrimSpace(head.Output)
	tip, ok := written[old]
	if !ok {
		return "", fmt.Errorf("HEAD %s is not among the commits named for the ticket", shortSHA(old))
	}
	if _, err := d.wsGit(ctx, r, dir, log, "update-ref", "-m", "graft: name the ticket", "HEAD", tip, old); err != nil {
		return "", err
	}
	fmt.Fprintf(log, "named ticket #%d under %d commit messages\n", n, lacking)
	return "", nil
}

// lackingTicket lists the commits of rng whose message does not name the
// request's ticket, each as its short hash and subject.
func (d *Desk) lackingTicket(ctx context.Context, r *store.Request, dir, rng string, log io.Writer) ([]string, error) {
	commits, err := d.readCommits(ctx, r, dir, rng, log)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range commits {
		if !namesTicket(c.msg, r.Ticket.Number) {
			out = append(out, shortSHA(c.sha)+" "+firstLine(c.msg))
		}
	}
	return out, nil
}
