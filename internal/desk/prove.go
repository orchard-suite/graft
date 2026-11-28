package desk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/store"
)

// A repository that releases with release-damnit verifies a landing proof:
// a statement that its check passed on the tree that landed, signed by a
// key the estate trusts. release-damnit land would run that check itself,
// in the runner's clone, beside the forge credential and the signing key,
// and the check is the code the agent wrote. So Graft splits it: the check
// runs in a sandbox, on exactly the tree that lands; the runner signs the
// result with release-damnit attest and lands it with release-damnit land
// --proven, which runs no check. When main moves meanwhile, the runner
// rebases and the loop starts over on the new tree (GRF-2007).

// landAttempts is how many times a landing starts over when main moves.
const landAttempts = 5

// exitMainMoved is release-damnit land --proven's exit code when the
// forge's main moved, so the proven commit no longer lands as it is.
const exitMainMoved = 3

// errNoSigningKey stops a landing that needs a proof Graft cannot sign.
var errNoSigningKey = errors.New("this repository's landings need a signed proof, and Graft has no key to sign one: set GRAFT_SIGNING_KEY_FILE to an SSH private key file whose public half the estate's allowed signers list. Nothing was pushed")

// landProven lands the runner's main with a proof of its check.
func (d *Desk) landProven(ctx context.Context, r *store.Request, log io.Writer) error {
	dir := d.repoDir(r)
	for attempt := 1; ; attempt++ {
		check, err := d.landingCheck(ctx, r)
		if err != nil {
			return err
		}
		if check != "" {
			if err := d.proveLanding(ctx, r, log, check); err != nil {
				return err
			}
		}
		d.step(r, store.StepDid, "Landing on main with `release-damnit land --proven`.", nil)
		_, err = shell.Run(ctx, log, shell.Cmd{Dir: dir, Args: []string{"release-damnit", "land", "--proven"}})
		var exit *shell.Error
		if err == nil || !errors.As(err, &exit) || exit.Code != exitMainMoved {
			return err
		}
		if attempt >= landAttempts {
			return fmt.Errorf("main kept moving; gave up landing after %d attempts", attempt)
		}
		fmt.Fprintf(log, "main moved while landing; rebasing and checking again (attempt %d of %d)\n", attempt+1, landAttempts)
		if _, err := d.git(ctx, dir, log, "fetch", "--quiet", "origin"); err != nil {
			return err
		}
		if err := d.onMain(ctx, r, log); err != nil {
			return err
		}
	}
}

// proveLanding runs check on the tree the runner's main holds, in a
// sandbox that holds no key, and signs its passing in the runner. A check
// that fails there goes back to the agent: main and the agent's commits do
// not pass together.
func (d *Desk) proveLanding(ctx context.Context, r *store.Request, log io.Writer, check string) error {
	if d.cfg.SigningKeyFile == "" {
		return errNoSigningKey
	}
	if err := d.enter(ctx, r, "land"); err != nil {
		return err
	}
	defer d.shut(ctx, r)
	if err := d.seed(ctx, r, log); err != nil {
		return err
	}
	dir := d.dir(r)
	want, err := d.rev(ctx, r, log, "HEAD^{tree}")
	if err != nil {
		return err
	}
	got, err := d.wsGit(ctx, r, dir, log, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(got.Output) != want {
		return fmt.Errorf("the sandbox's clone holds tree %s, not the tree that lands, %s", shortSHA(strings.TrimSpace(got.Output)), shortSHA(want))
	}
	d.prepare(ctx, r, dir, log)
	d.step(r, store.StepDid, "Running `"+check+"` on the tree that lands, in a sandbox.", nil)
	if _, err := d.box(r).WS.Run(ctx, log, shell.Cmd{Dir: dir, Args: []string{"sh", "-c", check}}); err != nil {
		return &backToAgent{why: "the repository's check failed on the tree that lands", prompt: landCheckBrief(check, err.Error())}
	}
	repo := d.repoDir(r)
	for _, kv := range [][2]string{{"gpg.format", "ssh"}, {"user.signingkey", d.cfg.SigningKeyFile}} {
		if _, err := d.git(ctx, repo, log, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	_, err = shell.Run(ctx, log, shell.Cmd{Dir: repo, Args: []string{"release-damnit", "attest"}})
	return err
}

// maxReleaseConfig is the most of a release config the runner reads.
const maxReleaseConfig = 64 << 10

// landingCheck is the check the release config at the runner's HEAD names,
// read as data, or "" when it names none. It reads the config the way
// release-damnit does, so the command that runs is the one the proof names.
func (d *Desk) landingCheck(ctx context.Context, r *store.Request) (string, error) {
	res, err := d.git(ctx, d.repoDir(r), io.Discard, "cat-file", "-s", "HEAD:release-please-config.json")
	if err != nil {
		return "", nil
	}
	if size, err := strconv.Atoi(strings.TrimSpace(res.Output)); err != nil || size > maxReleaseConfig {
		return "", fmt.Errorf("release-please-config.json is larger than %d bytes", maxReleaseConfig)
	}
	res, err = d.git(ctx, d.repoDir(r), io.Discard, "cat-file", "blob", "HEAD:release-please-config.json")
	if err != nil {
		return "", err
	}
	var c struct {
		Check string `json:"check"`
	}
	if err := json.Unmarshal([]byte(res.Output), &c); err != nil {
		return "", fmt.Errorf("release-please-config.json does not parse: %w", err)
	}
	check := strings.TrimSpace(c.Check)
	if strings.ContainsAny(check, "\r\n") {
		return "", fmt.Errorf("release-please-config.json's check must be one command line")
	}
	return check, nil
}

func landCheckBrief(check, report string) string {
	return fmt.Sprintf("Graft put your commits on the newest main, and the repository's check, `%s`, failed on the result. Fix it with new commits, run the check again, commit, and stop. Never push.\n\n%s", check, shell.Tail(report, 8000))
}
