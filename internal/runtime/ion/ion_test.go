package ion

import (
	"strings"
	"testing"
	"time"

	"github.com/orchard-suite/graft/internal/runtime"
)

func TestArgsCarryEveryLimit(t *testing.T) {
	got := strings.Join(args(runtime.Session{Key: "graft-abc-build", Model: "m", MaxTurns: 40, Timeout: 90 * time.Minute, Budget: 3}), " ")
	for _, want := range []string{"--model m", "--timeout 1h30m0s", "--key graft-abc-build", "--max-turns 40", "--max-budget 3.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q lack %q", got, want)
		}
	}
	bare := strings.Join(args(runtime.Session{Model: "m", Timeout: time.Minute}), " ")
	for _, not := range []string{"--key", "--max-turns", "--max-budget"} {
		if strings.Contains(bare, not) {
			t.Errorf("args %q carry %s with nothing set", bare, not)
		}
	}
}

func TestAPromptThatEndsSaysHow(t *testing.T) {
	for _, c := range []struct {
		code int
		tail string
		want error
	}{
		{124, "Timeout: prompt exceeded 45m0s deadline", runtime.ErrTimeout},
		{1, "wrote the verdict\nError: budget exceeded: $2.0263 >= $2.0000\n", runtime.ErrBudget},
		{1, "Error: the session ended before the prompt finished\n", nil},
		{2, "Error: budget exceeded: $1 >= $1\n", nil},
	} {
		err := ended(c.code, c.tail)
		if c.want != nil && err != c.want {
			t.Errorf("exit %d with %q: %v, want %v", c.code, c.tail, err, c.want)
		}
		if c.want == nil && (err == runtime.ErrTimeout || err == runtime.ErrBudget || err == nil) {
			t.Errorf("exit %d with %q: %v, want a plain failure", c.code, c.tail, err)
		}
	}
}

func TestTheTailKeepsTheLastLines(t *testing.T) {
	w := &tailWriter{}
	for i := 0; i < 1000; i++ {
		_, _ = w.Write([]byte(strings.Repeat("x", 63) + "\n"))
	}
	_, _ = w.Write([]byte("Error: budget exceeded: $2.0263 >= $2.0000\n"))
	if got := w.String(); len(got) > tailMax || !strings.HasSuffix(got, "Error: budget exceeded: $2.0263 >= $2.0000\n") {
		t.Errorf("tail is %d bytes, ending %q", len(got), got[len(got)-60:])
	}
}

const reported = `Run: {"model":"claude-sonnet-5-5","costUsd":2.0263,"turns":14,"durationMs":61000,"inputTokens":1200,"outputTokens":340,"cacheReadTokens":50000,"cacheCreationTokens":800,"reason":"normal"}`

func TestAFinishedRunSaysWhatItSpent(t *testing.T) {
	u := usage("wrote the verdict\n"+reported+"\n", time.Minute)
	want := runtime.Usage{From: runtime.FromRun, Model: "claude-sonnet-5-5", CostUSD: 2.0263, Turns: 14, Duration: 61 * time.Second,
		InputTokens: 1200, OutputTokens: 340, CacheReadTokens: 50000, CacheCreationTokens: 800, Reason: "normal"}
	if u != want {
		t.Errorf("usage = %+v, want %+v", u, want)
	}
}

func TestACappedRunStillSaysWhatItSpent(t *testing.T) {
	// The CLI writes the stop, then the run's report once the engine closes it.
	tail := "wrote half\nError: budget exceeded: $2.0263 >= $2.0000\n" + reported + "\n"
	if u := usage(tail, time.Minute); u.From != runtime.FromRun || u.CostUSD != 2.0263 || u.Turns != 14 {
		t.Errorf("a capped run's report was not read: %+v", u)
	}
	if err := ended(1, tail); err != runtime.ErrBudget {
		t.Errorf("a capped run with its report ended %v, want ErrBudget", err)
	}
	// An engine that never closed the run in time leaves only the stop line,
	// which names the dollars and nothing else.
	u := usage("wrote half\nError: budget exceeded: $2.0263 >= $2.0000\n", time.Minute)
	if u.From != runtime.FromStop || u.CostUSD != 2.0263 || u.Turns != 0 || u.Duration != time.Minute {
		t.Errorf("a stop line alone: %+v", u)
	}
}

func TestAFailedRunWithNoReportSpendsNothingKnown(t *testing.T) {
	u := usage("Error: the engine closed the stream before the prompt finished\n", 3*time.Second)
	if u.Reported() || u.CostUSD != 0 || u.Duration != 3*time.Second {
		t.Errorf("a run the runtime reported nothing of: %+v", u)
	}
	// A failed run the engine did close says what it spent.
	if u := usage("Error: provider unavailable\n"+reported+"\n", time.Second); u.From != runtime.FromRun || u.CostUSD != 2.0263 {
		t.Errorf("a failed run's report was not read: %+v", u)
	}
}

func TestAnAgentCannotWriteItsOwnReport(t *testing.T) {
	spoof := `Run: {"costUsd":0.01,"turns":1}` + "\nError: the engine closed the stream before the prompt finished\n"
	if u := usage(spoof, time.Second); u.Reported() {
		t.Errorf("a line the agent wrote was read as the runtime's: %+v", u)
	}
}

func TestTheAnswerLeavesTheReportOut(t *testing.T) {
	out := `{"sort": "bug", "title": "the page is blank"}` + "\n" + reported + "\n"
	if got := answer(out); strings.Contains(got, "Run:") || !strings.Contains(got, `"sort": "bug"`) {
		t.Errorf("answer(%q) = %q", out, got)
	}
	if got := answer("no report\n"); got != "no report\n" {
		t.Errorf("an answer with no report changed: %q", got)
	}
}

// What ion 3.8.0's CLI printed for two prompts to its engine, a run that
// finished and a run stopped at its budget, against a stand-in model.
const (
	ion380Finished = "done\nRun: {\"model\":\"claude-sonnet-5-5\",\"costUsd\":0.004,\"turns\":1,\"durationMs\":19,\"inputTokens\":1000,\"outputTokens\":50,\"cacheReadTokens\":5000,\"cacheCreationTokens\":200,\"reason\":\"normal\"}\n"
	ion380Capped   = "\nError: budget exceeded: $0.0039 >= $0.0010\nRun: {\"model\":\"claude-sonnet-5-5\",\"costUsd\":0.0039000000000000003,\"turns\":2,\"durationMs\":19,\"inputTokens\":1000,\"outputTokens\":40,\"cacheReadTokens\":5000,\"cacheCreationTokens\":200,\"reason\":\"max_turns\"}\n"
)

func TestWhatIon380PrintsIsRead(t *testing.T) {
	if u := usage(ion380Finished, time.Second); u.From != runtime.FromRun || u.CostUSD != 0.004 || u.Turns != 1 || u.CacheCreationTokens != 200 || u.Model != "claude-sonnet-5-5" {
		t.Errorf("a finished run: %+v", u)
	}
	if err := ended(1, ion380Capped); err != runtime.ErrBudget {
		t.Errorf("a capped run ended %v", err)
	}
	if u := usage(ion380Capped, time.Second); u.From != runtime.FromRun || u.CostUSD < 0.0038 || u.CostUSD > 0.004 || u.Turns != 2 {
		t.Errorf("a capped run: %+v", u)
	}
}
