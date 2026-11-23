package desk

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/forge"
	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/store"
)

// A builder stopped at its budget is never finished, whatever it says: its
// wind-down commits are kept on the request and never landed, the build
// stalls with what is left, and a retry goes on from those commits with the
// list. This is the request that landed unfinished work in 6.5.
func TestABuildStoppedAtItsBudgetKeepsItsWorkOnTheRequestAndNeverLands(t *testing.T) {
	fx, r := approvedFeature(t)
	var prompts []string
	fx.rt.build = func(dir, prompt string) error {
		prompts = append(prompts, prompt)
		switch {
		case len(prompts) == 1:
			write(t, dir, "a.txt", "one\ntwo\n")
			return runtime.ErrBudget
		case strings.HasPrefix(prompt, "Graft stopped you"):
			run(t, dir, "git", "add", "-A")
			run(t, dir, "git", "commit", "-q", "-m", "feat: add the second line")
			return sayFinish(dir, Finish{Done: true, Summary: "most of it", Left: []string{"The Forms seed files are not regenerated."}})
		}
		// The retry: the earlier commit is here, and it finishes.
		if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "one\ntwo\n" {
			return fmt.Errorf("the retry does not start from the kept work: %q", got)
		}
		write(t, dir, "b.txt", "seeds\n")
		run(t, dir, "git", "add", "-A")
		run(t, dir, "git", "commit", "-q", "-m", "feat: regenerate the seed files")
		return nil
	}
	r = fx.work(r)
	if r.State != store.StateStalled || r.Stall.Stage != store.StateBuilding {
		t.Fatalf("state %s, stall %+v", r.State, r.Stall)
	}
	if got := fx.remoteFile("a.txt"); got != "one\n" {
		t.Fatalf("unfinished work reached main: %q", got)
	}
	u := r.Unfinished
	if u == nil || u.Part != store.PartBuild || u.Head == "" || len(u.Left) != 2 || u.Left[0] != "The Forms seed files are not regenerated." {
		t.Fatalf("unfinished: %+v", u)
	}
	if kept := run(t, fx.d.repoDir(r), "git", "show", "refs/graft/agent:a.txt"); kept != "one\ntwo" {
		t.Errorf("the work is not kept in the runner's clone: %q", kept)
	}
	said := fx.lastComment()
	for _, want := range []string{"**Graft stopped while building.**", "The build is not finished: the builder ran past its $3.00 budget", "#### What is left",
		"- The Forms seed files are not regenerated.", "The work so far is kept on this request, up to `" + shortSHA(u.Head) + "`, and was not landed", "raise `limits.budget.build`"} {
		if !strings.Contains(said, want) {
			t.Errorf("the comment lacks %q:\n%s", want, said)
		}
	}
	if _, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateReleased || r.Unfinished != nil {
		t.Fatalf("the retry: state %s %s", r.State, r.Error)
	}
	if fx.remoteFile("a.txt") != "one\ntwo\n" || fx.remoteFile("b.txt") != "seeds\n" {
		t.Errorf("main after the retry: %q %q", fx.remoteFile("a.txt"), fx.remoteFile("b.txt"))
	}
	retry := prompts[len(prompts)-1]
	for _, want := range []string{"## Go on from the attempt before", "up to " + shortSHA(u.Head), "- The Forms seed files are not regenerated."} {
		if !strings.Contains(retry, want) {
			t.Errorf("the retry's builder is not told %q", want)
		}
	}
}

// A builder that stops without saying what it did is asked; one that says
// what is left is unfinished at once, and its gate never runs.
func TestABuildLandsOnlyWhenItsBuilderSaysItIsFinished(t *testing.T) {
	fx, r := approvedFeature(t)
	fx.rt.unsaid = true
	var prompts []string
	fx.rt.build = func(dir, prompt string) error {
		prompts = append(prompts, prompt)
		if len(prompts) == 1 {
			return fx.commitTwo(dir)
		}
		return sayFinish(dir, Finish{Left: []string{"Two browser tests fail: the save button is not found."}})
	}
	r = fx.work(r)
	if r.State != store.StateStalled || len(prompts) != 2 || fx.remoteFile("a.txt") != "one\n" {
		t.Fatalf("state %s after %d rounds; main %q", r.State, len(prompts), fx.remoteFile("a.txt"))
	}
	if !strings.Contains(prompts[1], "You did not say whether the work is finished") {
		t.Errorf("the gate does not ask the builder whether it is finished:\n%s", prompts[1])
	}
	if u := r.Unfinished; u == nil || len(u.Left) != 1 || !strings.Contains(u.Left[0], "Two browser tests fail") || u.Head == "" {
		t.Errorf("unfinished: %+v", u)
	}
}

// The builder is told the repository's commit rules and its own check, and
// the gate clears the build output tests leave behind instead of failing on
// it.
func TestTheBuilderIsToldTheRepositorysRulesAndTheGateClearsBuildOutput(t *testing.T) {
	// No ignore file of this machine's hides the output from git.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fx, r := approvedFeature(t)
	var prompt string
	fx.rt.build = func(dir, p string) error {
		prompt = p
		if err := fx.commitTwo(dir); err != nil {
			return err
		}
		write(t, dir, "tests/contracts/__pycache__/x.cpython-312.pyc", "bytes")
		write(t, dir, "TestResults/run.trx", "<xml/>")
		return nil
	}
	r = fx.work(r)
	if r.State != store.StateReleased {
		t.Fatalf("state %s: %s", r.State, r.Error)
	}
	for _, want := range []string{"## Before you say it is finished", "make check", "Leave the tree clean", "## Say when it is finished", "graft report done", "CLAUDE.md"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the builder is not told %q", want)
		}
	}
	var build string
	for _, c := range fx.forge.comments {
		if strings.HasPrefix(c, "### The build of round") {
			build = c
		}
	}
	if !strings.Contains(build, "Removed build output left in the clone: TestResults/, tests/contracts/__pycache__/") {
		t.Errorf("the build's comment does not say what the gate cleared:\n%s", build)
	}
}

func TestBuildOutputIsKnownByTheFolderItSitsIn(t *testing.T) {
	for p, want := range map[string]string{
		"tests/contracts/__pycache__/a.pyc": "tests/contracts/__pycache__/",
		"src/Api.Tests/TestResults/x.trx":   "src/Api.Tests/TestResults/",
		"web/test-results/shot.png":         "web/test-results/",
		"tools/gen.pyc":                     "tools/gen.pyc",
		"src/coverage.ts":                   "",
		"docs/new.md":                       "",
	} {
		got, ok := litterOf(p)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v, want %q", p, got, ok, want)
		}
	}
}

func TestAWaiverNamesACriterionOrTheScreenshotsAndSaysWhy(t *testing.T) {
	for body, want := range map[string]struct {
		n   int
		why string
		ok  bool
	}{
		"/graft waive 3 the e2e suite cannot run here":  {3, "the e2e suite cannot run here", true},
		"/graft waive screenshots needs a live sign-in": {store.WaiveShots, "needs a live sign-in", true},
		"/Graft Waive 2":          {2, "", true},
		"/graft waive 0 no":       {0, "", false},
		"/graft retry":            {0, "", false},
		"please /graft waive 3 x": {0, "", false},
	} {
		n, why, ok := readWaiver(body)
		if n != want.n || why != want.why || ok != want.ok {
			t.Errorf("%q: %d %q %v", body, n, why, ok)
		}
	}
}

// A pipeline that is not green released nothing: the request stalls at
// watching, its comment names each failed job with its page, and a retry
// reads the pipeline again and hands the builder each failed job's log to
// fix forward.
func TestARedPipelineReleasesNothingAndARetryFixesItForward(t *testing.T) {
	fx, r := approvedFeature(t)
	fx.rt.build = func(dir, prompt string) error {
		if strings.Contains(prompt, "the pipeline failed on them") {
			write(t, dir, "c.txt", "fixed\n")
			run(t, dir, "git", "add", "-A")
			run(t, dir, "git", "commit", "-q", "-m", "fix: the orders test")
			return nil
		}
		return fx.commitTwo(dir)
	}
	var mu sync.Mutex
	fx.forge.runs = func(sha string) []forge.Run {
		mu.Lock()
		defer mu.Unlock()
		if msg := run(t, fx.remote, "git", "log", "-1", "--format=%s", sha); strings.HasPrefix(msg, "fix: the orders test") {
			return []forge.Run{{ID: "3", Name: "pipeline (push)", URL: "https://gitlab.example.com/p/3", Status: "completed", Conclusion: "success"}}
		}
		return []forge.Run{{ID: "1", Name: "pipeline (push)", URL: "https://gitlab.example.com/p/1", Status: "completed", Conclusion: "failure"},
			{ID: "2", Name: "release", URL: "https://gitlab.example.com/p/2", Status: "completed", Conclusion: "cancelled"}}
	}
	r = fx.work(r)
	if r.State != store.StateStalled || r.Stall.Stage != store.StateWatching {
		t.Fatalf("state %s, stall %+v", r.State, r.Stall)
	}
	if len(fx.inc.changes) != 0 || len(fx.forge.closed) != 0 {
		t.Errorf("a red pipeline was recorded as a release: changes %+v, closed %v", fx.inc.changes, fx.forge.closed)
	}
	said := fx.lastComment()
	for _, want := range []string{"**Graft stopped while watching the release.**", "is not green, so nothing was released: pipeline (push) ended failure; release ended cancelled",
		"- [pipeline (push)](https://gitlab.example.com/p/1) ended failure", "  - job [test (test)](https://gitlab.example.com/p/1/jobs/test) failed", "- [release](https://gitlab.example.com/p/2) ended cancelled"} {
		if !strings.Contains(said, want) {
			t.Errorf("the comment lacks %q:\n%s", want, said)
		}
	}
	if strings.Contains(strings.Join(fx.forge.comments, "\n"), "Released:") {
		t.Error("the ticket says it was released")
	}
	var prompts []string
	build := fx.rt.build
	fx.rt.build = func(dir, prompt string) error {
		prompts = append(prompts, prompt)
		return build(dir, prompt)
	}
	if _, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	r = fx.work(r)
	if r.State != store.StateReleased || fx.remoteFile("c.txt") != "fixed\n" {
		t.Fatalf("after the retry: state %s: %s", r.State, r.Error)
	}
	if len(prompts) != 1 || !strings.Contains(prompts[0], "---- job test (test) ----") || !strings.Contains(prompts[0], "FAIL TestOrders") {
		t.Errorf("the retry's builder is not handed the failed job's log:\n%s", strings.Join(prompts, "\n----\n"))
	}
}

// A pipeline that was only stopped has no failed job to fix: the request
// stalls, and a retry after it is run again on the forge releases.
func TestACancelledPipelineStallsUntilItIsRunAgain(t *testing.T) {
	fx, r := approvedFeature(t)
	fx.rt.build = func(dir, _ string) error { return fx.commitTwo(dir) }
	var mu sync.Mutex
	conclusion := "cancelled"
	fx.forge.runs = func(string) []forge.Run {
		mu.Lock()
		defer mu.Unlock()
		return []forge.Run{{ID: "1", Name: "pipeline (push)", URL: "https://gitlab.example.com/p/1", Status: "completed", Conclusion: conclusion}}
	}
	r = fx.work(r)
	if r.State != store.StateStalled || !strings.Contains(fx.lastComment(), "No job failed: the pipeline was stopped or waits for a person.") {
		t.Fatalf("state %s:\n%s", r.State, fx.lastComment())
	}
	mu.Lock()
	conclusion = "success"
	mu.Unlock()
	builds := len(fx.rt.prompts)
	if _, err := fx.d.Retry(context.Background(), r.ID, "lead@example.com"); err != nil {
		t.Fatal(err)
	}
	if r = fx.work(r); r.State != store.StateReleased {
		t.Fatalf("after the pipeline ran again: state %s", r.State)
	}
	if len(fx.rt.prompts) != builds {
		t.Error("a pipeline that only needed running again went back to the builder")
	}
}

// Where the repository's release job writes a release commit after the
// landed one, its pipeline is the release: the landed commit's own green
// pipeline is not enough.
func TestOnlyTheReleaseCommitsGreenPipelineIsARelease(t *testing.T) {
	for _, releaseGreen := range []bool{false, true} {
		t.Run(fmt.Sprint("release green ", releaseGreen), func(t *testing.T) {
			fx, r := approvedFeature(t)
			releaseDamnit(t, fx)
			fx.rt.build = func(dir, _ string) error { return fx.commitTwo(dir) }
			var mu sync.Mutex
			released := ""
			fx.forge.runs = func(sha string) []forge.Run {
				mu.Lock()
				defer mu.Unlock()
				if sha == released {
					end := "failure"
					if releaseGreen {
						end = "success"
					}
					return []forge.Run{{ID: "9", Name: "pipeline (push)", URL: "https://gitlab.example.com/p/9", Status: "completed", Conclusion: end}}
				}
				if released == "" {
					// The landed commit's release job writes the release commit.
					run(t, fx.seed, "git", "pull", "-q", "origin", "main")
					run(t, fx.seed, "git", "commit", "-q", "--allow-empty", "-m", "chore: release versions")
					run(t, fx.seed, "git", "push", "-q", "origin", "HEAD:main")
					released = run(t, fx.seed, "git", "rev-parse", "HEAD")
				}
				return []forge.Run{{ID: "8", Name: "pipeline (push)", URL: "https://gitlab.example.com/p/8", Status: "completed", Conclusion: "success"}}
			}
			r = fx.work(r)
			if r.Release == "" || r.Release != released {
				t.Fatalf("the release commit %q was not watched: %q", released, r.Release)
			}
			if !releaseGreen {
				if r.State != store.StateStalled || !strings.Contains(fx.lastComment(), "The pipeline of ["+shortSHA(released)+"]") {
					t.Fatalf("a red release pipeline: state %s\n%s", r.State, fx.lastComment())
				}
				return
			}
			if r.State != store.StateReleased || !strings.Contains(strings.Join(fx.forge.comments, "\n"), "Released: "+fx.forge.CommitURL(r.Repo, released)) {
				t.Fatalf("a green release pipeline: state %s", r.State)
			}
		})
	}
}

// releaseDamnit makes the fixture's repository one that lands with
// release-damnit, and puts a release-damnit on the PATH that lands by
// pushing main and passes every check.
func releaseDamnit(t *testing.T, fx *fixture) {
	t.Helper()
	bin := t.TempDir()
	write(t, bin, "release-damnit", "#!/bin/sh\ncase \"$1\" in land) exec git push -q origin HEAD:main ;; esac\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "release-damnit"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	run(t, fx.seed, "git", "pull", "-q", "origin", "main")
	write(t, fx.seed, "release-please-config.json", `{"packages": {".": {}}, "subject-max-length": 51}`)
	run(t, fx.seed, "git", "add", "-A")
	run(t, fx.seed, "git", "commit", "-q", "-m", "chore: release with release-damnit")
	run(t, fx.seed, "git", "push", "-q", "origin", "HEAD:main")
}

var _ = orrery.RunQueued

// Before the builder starts, Graft installs the browser each Playwright
// version the repository pins wants: the lockfile's version where there is
// one, else the version the manifest names.
func TestTheSandboxGetsTheBrowserTheRepositorysPlaywrightWants(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	fx, r := approvedFeature(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	write(t, bin, "npx", "#!/bin/sh\necho \"$@\" >> "+calls+"\n")
	if err := os.Chmod(filepath.Join(bin, "npx"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	run(t, fx.seed, "git", "pull", "-q", "origin", "main")
	write(t, fx.seed, "package.json", `{"devDependencies": {"@playwright/test": "^1.62.0"}}`)
	write(t, fx.seed, "package-lock.json", `{"packages": {"node_modules/@playwright/test": {"version": "1.62.1"}}}`)
	write(t, fx.seed, "web/package.json", `{"devDependencies": {"playwright": "1.58.0"}}`)
	run(t, fx.seed, "git", "add", "-A")
	run(t, fx.seed, "git", "commit", "-q", "-m", "chore: add playwright")
	run(t, fx.seed, "git", "push", "-q", "origin", "HEAD:main")
	fx.rt.build = func(dir, _ string) error { return fx.commitTwo(dir) }
	if r = fx.work(r); r.State != store.StateReleased {
		t.Fatalf("state %s: %s", r.State, r.Error)
	}
	got, _ := os.ReadFile(calls)
	for _, want := range []string{"--yes playwright@1.62.1 install chromium", "--yes playwright@1.58.0 install chromium"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("npx was not asked %q:\n%s", want, got)
		}
	}
	if !strings.Contains(strings.Join(fx.forge.comments, "\n"), "Installed the browser Playwright 1.62.1 wants.") {
		t.Error("the build's comment does not say the browser was installed")
	}
}

// A wind-down that says the work is done is still a run stopped at its
// budget: the build stalls, and nothing lands.
func TestAWindDownThatSaysItIsDoneIsStillUnfinished(t *testing.T) {
	fx, r := approvedFeature(t)
	fx.rt.build = func(dir, prompt string) error {
		if strings.HasPrefix(prompt, "Graft stopped you") {
			if err := fx.commitTwo(dir); err != nil {
				return err
			}
			return sayFinish(dir, Finish{Done: true, Summary: "all of it"})
		}
		return runtime.ErrBudget
	}
	if r = fx.work(r); r.State != store.StateStalled || r.Unfinished == nil || fx.remoteFile("a.txt") != "one\n" {
		t.Fatalf("a capped build that said done: state %s, main %q", r.State, fx.remoteFile("a.txt"))
	}
}
