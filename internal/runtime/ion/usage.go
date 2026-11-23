package ion

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/runtime"
)

// reportPrefix starts the line the ion CLI ends a prompt with once the
// engine closed its run: the run's report as one JSON object, on stderr,
// after any error line. An ion before 3.8 writes none.
const reportPrefix = "Run: "

// report is the ion CLI's run report, as it prints it.
type report struct {
	Model               string  `json:"model"`
	CostUsd             float64 `json:"costUsd"`
	Turns               int     `json:"turns"`
	DurationMs          int64   `json:"durationMs"`
	InputTokens         int     `json:"inputTokens"`
	OutputTokens        int     `json:"outputTokens"`
	CacheReadTokens     int     `json:"cacheReadTokens"`
	CacheCreationTokens int     `json:"cacheCreationTokens"`
	Reason              string  `json:"reason"`
}

// spentLine is the figure in the CLI's line for a run the engine stopped at
// its budget: "Error: budget exceeded: $2.0263 >= $2.0000".
var spentLine = regexp.MustCompile(regexp.QuoteMeta(budgetLine) + ` \$([0-9]+(?:\.[0-9]+)?) >=`)

// usage reads what a prompt's run consumed from the end of the CLI's
// output: its report, which ion prints last and only once the engine closed
// the run, or, without one, what the engine spent when it stopped the run at
// its budget. A run the CLI reported nothing of has only the time it took,
// which Graft measured itself.
func usage(tail string, took time.Duration) runtime.Usage {
	if r, ok := lastReport(tail); ok {
		u := runtime.Usage{From: runtime.FromRun, Model: r.Model, CostUSD: r.CostUsd, Turns: r.Turns,
			InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens, CacheCreationTokens: r.CacheCreationTokens,
			Duration: time.Duration(r.DurationMs) * time.Millisecond, Reason: r.Reason}
		if u.Duration <= 0 {
			u.Duration = took
		}
		return u
	}
	u := runtime.Usage{Duration: took}
	if m := spentLine.FindAllStringSubmatch(tail, -1); len(m) > 0 {
		if cost, err := strconv.ParseFloat(m[len(m)-1][1], 64); err == nil {
			u.From, u.CostUSD = runtime.FromStop, cost
		}
	}
	return u
}

// lastReport is the run report on the output's last line. The agent's own
// words come before it, so only the very last line counts: a line an agent
// wrote that looks like one is never read as the runtime's.
func lastReport(tail string) (report, bool) {
	lines := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if !strings.HasPrefix(last, reportPrefix+"{") {
		return report{}, false
	}
	var r report
	if err := json.Unmarshal([]byte(strings.TrimPrefix(last, reportPrefix)), &r); err != nil {
		return report{}, false
	}
	return r, true
}

// answer is what a model wrote, without the runtime's report after it.
func answer(out string) string {
	if _, ok := lastReport(out); !ok {
		return out
	}
	trimmed := strings.TrimRight(out, "\n")
	return trimmed[:strings.LastIndexByte(trimmed, '\n')+1]
}
