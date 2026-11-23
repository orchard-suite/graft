package desk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orchard-suite/graft/internal/store"
)

// commitMsgHook gives the builder's clone a commit-msg hook outside the
// repository, the way husky points git at its own, and returns where it is.
func commitMsgHook(t *testing.T, script string) string {
	t.Helper()
	hooks := t.TempDir()
	p := filepath.Join(hooks, "commit-msg")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return hooks
}

func (fx *fixture) buildPrompt() string {
	fx.t.Helper()
	for _, p := range fx.rt.prompts {
		if key, _, _ := strings.Cut(p, "\n"); strings.HasSuffix(key, "-build") {
			return p
		}
	}
	fx.t.Fatal("the builder was never prompted")
	return ""
}

func TestGraftNamesTheTicketUnderEachCommitAndKeepsTheSubject(t *testing.T) {
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("a.txt", "tests/two.sh"))
	seen := filepath.Join(t.TempDir(), "seen")
	// The repository's own rule: a subject of 51 characters at most. The
	// hook keeps every message it passed.
	hooks := commitMsgHook(t, `subject=$(head -n 1 "$1")
[ ${#subject} -le 51 ] || { echo "subject over 51: $subject"; exit 1; }
cat "$1" >> `+seen+`
`)
	subject := "fix: show the second line on the orders page please"
	if len(subject) != 51 {
		t.Fatalf("the subject is %d characters, want 51", len(subject))
	}
	var authored string
	fx.rt.build = func(dir, _ string) error {
		run(t, dir, "git", "config", "core.hooksPath", hooks)
		write(t, dir, "a.txt", "one\ntwo\n")
		run(t, dir, "git", "add", "a.txt")
		run(t, dir, "git", "commit", "-q", "--date=1700000000 +0100", "-m", subject+"\n\nThe page reads the second line.")
		write(t, dir, "tests/two.sh", "grep -q two a.txt\n")
		run(t, dir, "git", "add", "-A")
		run(t, dir, "git", "commit", "-q", "-m", "fix: prove the second line\n\nTicket #1.")
		authored = run(t, dir, "git", "log", "-1", "--format=%an <%ae> %ad", "--date=raw", "HEAD~1")
		return nil
	}
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateReleased || r.Rounds != 1 {
		log, _ := os.ReadFile(fx.d.LogPath(r.ID))
		t.Fatalf("state %s after %d rounds, want released after one\n%s", r.State, r.Rounds, log)
	}
	if p := fx.buildPrompt(); !strings.Contains(p, "Graft names the ticket under every commit message itself, as the trailer `Refs: #1`") || strings.Contains(p, "must name the ticket") {
		t.Errorf("the builder is not told Graft names the ticket:\n%s", p)
	}

	// Newest first: the commit that named the ticket itself, then the one
	// Graft named it under.
	log := run(t, fx.remote, "git", "log", "--format=%s%x00%B%x00%an <%ae> %ad%x01", "--date=raw", "main~2..main")
	var got [][]string
	for _, e := range strings.Split(log, "\x01") {
		if e = strings.TrimLeft(e, "\n"); e != "" {
			got = append(got, strings.Split(e, "\x00"))
		}
	}
	if len(got) != 2 {
		t.Fatalf("main holds %d commits of the request, want 2:\n%q", len(got), log)
	}
	if got[0][1] != "fix: prove the second line\n\nTicket #1.\n" {
		t.Errorf("a commit that named the ticket was given another message: %q", got[0][1])
	}
	if got[1][0] != subject || len(got[1][0]) != 51 {
		t.Errorf("the subject changed: %q", got[1][0])
	}
	if got[1][1] != subject+"\n\nThe page reads the second line.\n\nRefs: #1\n" {
		t.Errorf("the ticket is not a trailer under the message: %q", got[1][1])
	}
	if got[1][2] != authored {
		t.Errorf("the author or the date changed: %q, was %q", got[1][2], authored)
	}
	kept, _ := os.ReadFile(seen)
	if !strings.Contains(string(kept), subject+"\n\nThe page reads the second line.\n\nRefs: #1\n") {
		t.Errorf("the repository's hook never judged the message Graft wrote:\n%s", kept)
	}
}

func TestAHookThatRefusesTheTrailerSendsTheBuilderToNameTheTicketItself(t *testing.T) {
	fx := newFixture(t)
	fx.findings(defect(), fixPlan("a.txt", "tests/two.sh"))
	hooks := commitMsgHook(t, `! grep -q '^Refs:' "$1" || { echo "this repository takes no Refs trailer"; exit 1; }
`)
	rounds := 0
	fx.rt.build = func(dir, prompt string) error {
		rounds++
		run(t, dir, "git", "config", "core.hooksPath", hooks)
		if rounds == 1 {
			write(t, dir, "a.txt", "one\ntwo\n")
			write(t, dir, "tests/two.sh", "grep -q two a.txt\n")
			run(t, dir, "git", "add", "-A")
			run(t, dir, "git", "commit", "-q", "-m", "fix: add the second line")
			return nil
		}
		if !strings.Contains(prompt, "this repository takes no Refs trailer") || !strings.Contains(prompt, "Name the ticket as `#1` in that message yourself") {
			t.Errorf("the second round does not say why: %s", prompt)
		}
		run(t, dir, "git", "commit", "-q", "--amend", "-m", "fix: add the second line\n\nTicket #1.")
		return nil
	}
	r := fx.work(fx.submit("developer", "the orders page is blank when I open it"))
	if r.State != store.StateReleased || rounds != 2 {
		log, _ := os.ReadFile(fx.d.LogPath(r.ID))
		t.Fatalf("state %s after %d rounds\n%s", r.State, rounds, log)
	}
	if got := run(t, fx.remote, "git", "log", "-1", "--format=%B", "main"); got != "fix: add the second line\n\nTicket #1." {
		t.Errorf("main holds %q", got)
	}
}
