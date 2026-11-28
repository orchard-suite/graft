package desk

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/orchard-suite/graft/internal/store"
)

var (
	realOnce sync.Once
	realBin  string
	realErr  error
)

// realReleaseDamnit puts on the PATH the release-damnit the image bundles,
// at the version the Dockerfile pins, and makes the fixture's repository
// one that releases with it, with a check that also writes where it ran
// to the returned file.
func realReleaseDamnit(t *testing.T, fx *fixture) (runs string) {
	t.Helper()
	realOnce.Do(func() {
		var data []byte
		if data, realErr = os.ReadFile("../../Dockerfile"); realErr != nil {
			return
		}
		m := regexp.MustCompile(`(?m)^ARG RELEASE_DAMNIT_VERSION=(\S+)$`).FindSubmatch(data)
		if m == nil {
			realErr = os.ErrNotExist
			return
		}
		var dir string
		if dir, realErr = os.MkdirTemp("", "graft-release-damnit-"); realErr != nil {
			return
		}
		cmd := exec.Command("go", "install", "github.com/dsswift/release-damnit/cmd/release-damnit@v"+string(m[1]))
		cmd.Env = append(os.Environ(), "GOBIN="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			realErr = err
			t.Logf("%s", out)
			return
		}
		realBin = dir
	})
	if realErr != nil {
		t.Fatalf("install the Dockerfile's release-damnit: %v", realErr)
	}
	t.Setenv("PATH", realBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runs = filepath.Join(t.TempDir(), "runs")
	run(t, fx.seed, "git", "pull", "-q", "origin", "main")
	write(t, fx.seed, "release-please-config.json", `{"check": "pwd >> `+runs+` && make check", "packages": {".": {"component": "app"}}}`)
	write(t, fx.seed, "release-please-manifest.json", `{".": "1.0.0"}`)
	run(t, fx.seed, "git", "add", "-A")
	run(t, fx.seed, "git", "commit", "-q", "-m", "chore: release with release-damnit")
	run(t, fx.seed, "git", "push", "-q", "origin", "HEAD:main")
	return runs
}

// landingKey makes an SSH key for the runner and returns its private key
// file and an allowed signers file that lists it.
func landingKey(t *testing.T) (key, allowed string) {
	t.Helper()
	dir := t.TempDir()
	key = filepath.Join(dir, "landing")
	run(t, "", "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "graft", "-f", key)
	fp := strings.Fields(run(t, "", "ssh-keygen", "-l", "-E", "sha256", "-f", key))[1]
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed = filepath.Join(dir, "allowed_signers")
	write(t, dir, "allowed_signers", fp+` namespaces="release-damnit-landing" `+strings.TrimSpace(string(pub))+"\n")
	return key, allowed
}

// within says every line of the check's record of where it ran is under
// root: the check ran there and nowhere else.
func within(t *testing.T, runs, root string) []string {
	t.Helper()
	data, err := os.ReadFile(runs)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(root)
	var outside []string
	for _, dir := range strings.Fields(string(data)) {
		if !strings.HasPrefix(dir, root) && !strings.HasPrefix(dir, real) {
			outside = append(outside, dir)
		}
	}
	return outside
}

// A repository that verifies its landings gets a proof release-damnit
// verify accepts, from a check that ran in the sandbox and never in the
// runner's clone, beside the forge credential and the key.
func TestALandingWithAKeyCarriesAProofVerifyAccepts(t *testing.T) {
	fx, r := approvedFeature(t)
	runs := realReleaseDamnit(t, fx)
	key, allowed := landingKey(t)
	fx.d.cfg.SigningKeyFile = key
	fx.rt.build = func(dir, _ string) error { return fx.commitTwo(dir) }

	if r = fx.work(r); r.State != store.StateReleased {
		log, _ := os.ReadFile(fx.d.LogPath(r.ID))
		t.Fatalf("state %s: %s\n%s", r.State, r.Error, log)
	}
	if outside := within(t, runs, fx.d.cfg.WorkDir); len(outside) > 0 {
		t.Errorf("the landing check ran outside the sandbox's work folder: %v", outside)
	}
	if data, _ := os.ReadFile(runs); len(data) == 0 {
		t.Error("the landing check never ran")
	}
	ci := filepath.Join(t.TempDir(), "ci")
	run(t, "", "git", "clone", "-q", fx.remote, ci)
	if out := run(t, ci, "release-damnit", "verify", "--allowed-signers", allowed); !strings.Contains(out, `"pwd >> `+runs+` && make check" passed`) {
		t.Errorf("verify: %s", out)
	}
}

// With no key, a landing that needs a proof pushes nothing, runs no check,
// and says which setting gives Graft its key.
func TestALandingWithoutAKeyPushesNothingAndNamesTheSetting(t *testing.T) {
	fx, r := approvedFeature(t)
	runs := realReleaseDamnit(t, fx)
	fx.rt.build = func(dir, _ string) error { return fx.commitTwo(dir) }
	before := run(t, fx.remote, "git", "rev-parse", "main")

	fx.d.Run(t.Context(), r.ID)
	r = fx.reread(r)
	if r.State == store.StateReleased || r.State == store.StateWatching {
		t.Fatalf("landed with no key: state %s", r.State)
	}
	if !strings.Contains(r.Error, "GRAFT_SIGNING_KEY_FILE") || r.Stall == nil || !strings.Contains(r.Stall.Cause, "GRAFT_SIGNING_KEY_FILE") {
		t.Errorf("the failure does not name the setting: %q, %+v", r.Error, r.Stall)
	}
	if after := run(t, fx.remote, "git", "rev-parse", "main"); after != before {
		t.Errorf("main moved from %s to %s with no key", before, after)
	}
	if data, _ := os.ReadFile(runs); len(data) != 0 {
		t.Errorf("the landing check ran with no key to sign it: %s", data)
	}
}

// When main moves while Graft lands, the proven landing stops, and Graft
// rebases, checks the new tree in the sandbox again, signs it, and lands.
func TestALandingThatLosesARaceWithMainChecksTheNewTreeAndLands(t *testing.T) {
	fx, r := approvedFeature(t)
	runs := realReleaseDamnit(t, fx)
	key, allowed := landingKey(t)
	fx.d.cfg.SigningKeyFile = key
	fx.rt.build = func(dir, _ string) error { return fx.commitTwo(dir) }
	// The runner's first push to main loses to another commit pushed just
	// before it.
	hooks, marker := t.TempDir(), filepath.Join(t.TempDir(), "raced")
	write(t, hooks, "pre-push", `#!/bin/sh
case "$PWD" in "`+fx.d.cfg.RepoDir+`"*|"`+realPath(t, fx.d.cfg.RepoDir)+`"*) ;; *) exit 0 ;; esac
[ -f "`+marker+`" ] && exit 0
touch "`+marker+`"
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
cd "`+fx.seed+`" && git pull -q origin main && echo other > b.txt && git add b.txt && git commit -q -m "feat: add b" && git push -q origin HEAD:main
`)
	if err := os.Chmod(filepath.Join(hooks, "pre-push"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", hooks)

	if r = fx.work(r); r.State != store.StateReleased {
		log, _ := os.ReadFile(fx.d.LogPath(r.ID))
		t.Fatalf("state %s: %s\n%s", r.State, r.Error, log)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("main never moved under the landing")
	}
	data, _ := os.ReadFile(runs)
	if n := len(strings.Fields(string(data))); n != 2 {
		t.Errorf("the landing check ran %d times, want twice: the first tree, then the rebased one", n)
	}
	if outside := within(t, runs, fx.d.cfg.WorkDir); len(outside) > 0 {
		t.Errorf("the landing check ran outside the sandbox's work folder: %v", outside)
	}
	ci := filepath.Join(t.TempDir(), "ci")
	run(t, "", "git", "clone", "-q", fx.remote, ci)
	run(t, ci, "release-damnit", "verify", "--allowed-signers", allowed)
	if _, err := os.Stat(filepath.Join(ci, "b.txt")); err != nil {
		t.Error("the commit that won the race is not under the landing")
	}
}

func realPath(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
