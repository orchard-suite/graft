package desk

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/store"
	"github.com/orchard-suite/graft/internal/sweep"
)

// What a run's comment holds at most. The forges cap a comment (GitHub at
// 65,536 characters), and a person reads the top.
const (
	commentMax = 60000
	shownSteps = 60
	stepMax    = 300
	detailMax  = 1500
)

// renderRun is a run's comment, as Markdown: what it is and how it stands
// at the top, short; then what each criterion of the plan is proven by, the
// screenshots, the failure and what to do next; and every step, folded.
func renderRun(run *store.Run, v runView, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", runTitle(run))
	b.WriteString(standing(run, v, now))
	b.WriteString("\n\n")
	if run.Kind == store.RunRevise {
		if v.Revises != "" {
			fmt.Fprintf(&b, "Revises [round %d](%s).", run.Round-1, v.Revises)
		} else {
			fmt.Fprintf(&b, "Revises round %d.", run.Round-1)
		}
		if len(run.Answers) > 0 {
			b.WriteString(" It answers:\n\n")
			for _, a := range run.Answers {
				fmt.Fprintf(&b, "- %s\n", plain(a))
			}
		}
		b.WriteString("\n\n")
	}
	if run.Retries > 0 || run.RetriedBy != "" {
		what := "what failed"
		switch {
		case v.Retries != "":
			what = fmt.Sprintf("[the attempt before](%s)", v.Retries)
		case run.Retries > 0:
			what = fmt.Sprintf("the attempt before (run %d)", run.Retries)
		}
		fmt.Fprintf(&b, "Tries again %s", what)
		if run.RetriedBy != "" {
			fmt.Fprintf(&b, ", as %s asked on %s", plain(run.RetriedBy), run.Via)
		}
		b.WriteString(".")
		if v.Before != nil {
			fmt.Fprintf(&b, " It stopped: %s. This attempt was told why, and what it tried.", plain(sentence(v.Before.Cause)))
		}
		b.WriteString("\n\n")
	}
	if run.Open() {
		if s := lastStep(run); s != nil {
			fmt.Fprintf(&b, "**Now:** %s\n\n", plain(s.Text))
		}
	}
	if len(run.Criteria) > 0 && (len(run.Proofs) > 0 || !run.Open()) {
		b.WriteString(criteria(run))
	}
	if len(run.Shots) > 0 || run.NoShots != "" || waiverOf(run, store.WaiveShots) != nil {
		b.WriteString(shots(run))
	}
	if f := run.Failure; f != nil {
		b.WriteString(failureText(f))
	}
	if run.Closing != "" {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(run.Closing))
	}
	if len(run.Left) > 0 {
		b.WriteString("Left off this comment:\n\n")
		for _, l := range run.Left {
			fmt.Fprintf(&b, "- %s\n", plain(l))
		}
		b.WriteString("\n")
	}
	if v.Before != nil {
		fmt.Fprintf(&b, "<details><summary>What the attempt before hit</summary>\n\n%s</details>\n\n", failureText(v.Before))
	}
	b.WriteString(stepsText(run))
	out := b.String()
	if len(out) > commentMax {
		out = clip(out, commentMax) + "\n\n(This comment is cut at the forge's limit. Every step is on Graft's page.)"
	}
	return out
}

// runTitle names a run: what it does, for which round, and which attempt.
func runTitle(run *store.Run) string {
	var t string
	switch run.Kind {
	case store.RunInvestigate:
		t = fmt.Sprintf("Round %d of the plan: the investigation", run.Round)
	case store.RunRevise:
		t = fmt.Sprintf("Round %d of the plan: a revision", run.Round)
	case store.RunBuild:
		t = fmt.Sprintf("The build of round %d", run.Round)
		if run.Round == 0 {
			t = "The build, before anyone is asked: a clear defect in a small-fix lane"
		}
	case store.RunLand:
		t = "Landing"
	case store.RunRelease:
		t = "The release"
	default:
		t = run.Kind
	}
	if run.Attempt > 1 {
		t += fmt.Sprintf(" · attempt %d", run.Attempt)
	}
	return t
}

// standing is the run's state, its time, what its agents did, and where
// the request is, in one line, then how it ended. Dollars, its budget and
// its spend, are there only where the program shows cost on the ticket.
func standing(run *store.Run, v runView, now time.Time) string {
	var b strings.Builder
	switch {
	case run.Open():
		fmt.Fprintf(&b, "**Running**, %s so far", took(now.Sub(run.Started)))
	case run.Outcome == store.RunFailed:
		fmt.Fprintf(&b, "**Stopped** after %s", took(run.Ended.Sub(run.Started)))
	default:
		fmt.Fprintf(&b, "**Done** in %s", took(run.Ended.Sub(run.Started)))
	}
	fmt.Fprintf(&b, " · started %s", run.Started.UTC().Format("2006-01-02 15:04 UTC"))
	if t := run.Agents; t != nil && t.Prompts > 0 {
		fmt.Fprintf(&b, " · %s, %s", count(t.Prompts, "agent prompt"), count(t.Turns, "turn"))
		if v.Cost {
			fmt.Fprintf(&b, ", $%.2f spent", t.USD)
			if t.Unknown > 0 {
				fmt.Fprintf(&b, " (%s did not report its spend)", count(t.Unknown, "prompt"))
			}
		}
	}
	if v.Cost && run.Budget > 0 {
		fmt.Fprintf(&b, " · budget $%.2f an agent prompt", run.Budget)
	}
	if run.Session != "" {
		fmt.Fprintf(&b, " · session `%s`", run.Session)
	}
	if v.Request != "" {
		fmt.Fprintf(&b, " · [the request in Graft](%s)", v.Request)
	}
	if !run.Open() && run.Summary != "" && run.Outcome != store.RunFailed {
		fmt.Fprintf(&b, "\n\n%s", plain(run.Summary))
	}
	return b.String()
}

// count is n of a thing, its noun made plural past one.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// took is a span of time, in minutes or seconds.
func took(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// lastStep is what the run did last.
func lastStep(run *store.Run) *store.Step {
	if len(run.Steps) == 0 {
		return nil
	}
	return &run.Steps[len(run.Steps)-1]
}

// criteria lists what the plan says must hold, each with what proves it.
func criteria(run *store.Run) string {
	var b strings.Builder
	b.WriteString("#### Done when\n\n")
	shot := map[string]store.Shot{}
	for _, s := range run.Shots {
		shot[s.Name] = s
	}
	for i, c := range run.Criteria {
		var proofs []string
		for _, p := range run.Proofs {
			if p.Criterion != i+1 {
				continue
			}
			for _, name := range p.Shots {
				if s, ok := shot[name]; ok {
					proofs = append(proofs, fmt.Sprintf("screenshot: %s, %s", plain(s.Screen), s.State))
				}
			}
			if p.Test != "" {
				proofs = append(proofs, "test: `"+code(p.Test)+"`")
			}
		}
		if len(proofs) == 0 {
			if w := waiverOf(run, i+1); w != nil {
				fmt.Fprintf(&b, "- [x] %s (no proof: waived by %s, %s)\n", plain(c), plain(w.By), plain(w.Why))
				continue
			}
			fmt.Fprintf(&b, "- [ ] %s (no proof given)\n", plain(c))
			continue
		}
		fmt.Fprintf(&b, "- [x] %s (%s)\n", plain(c), strings.Join(proofs, "; "))
	}
	b.WriteString("\n")
	return b.String()
}

// waiverOf is the approvers' waiver of a criterion's proof on a run, or of
// its screenshots with store.WaiveShots, or nil.
func waiverOf(run *store.Run, criterion int) *store.Waiver {
	for i := range run.Waived {
		if run.Waived[i].Criterion == criterion {
			return &run.Waived[i]
		}
	}
	return nil
}

// shots shows the screenshots by screen, before beside after.
func shots(run *store.Run) string {
	var b strings.Builder
	b.WriteString("#### Screenshots\n\n")
	if len(run.Shots) == 0 {
		if w := waiverOf(run, store.WaiveShots); w != nil {
			fmt.Fprintf(&b, "None: waived by %s, %s.\n\n", plain(w.By), plain(strings.TrimRight(w.Why, ".")))
		} else {
			fmt.Fprintf(&b, "None: %s\n\n", plain(run.NoShots))
		}
		return b.String()
	}
	var screens []string
	by := map[string]map[string][]store.Shot{}
	for _, s := range run.Shots {
		if by[s.Screen] == nil {
			by[s.Screen] = map[string][]store.Shot{}
			screens = append(screens, s.Screen)
		}
		by[s.Screen][s.State] = append(by[s.Screen][s.State], s)
	}
	for _, screen := range screens {
		before, after := by[screen]["before"], by[screen]["after"]
		fmt.Fprintf(&b, "**%s**\n\n", plain(screen))
		if len(before) > 0 {
			b.WriteString("| Before | After |\n|---|---|\n")
			for i := 0; i < max(len(before), len(after)); i++ {
				fmt.Fprintf(&b, "| %s | %s |\n", picture(before, i), picture(after, i))
			}
			b.WriteString("\n")
		} else {
			for _, s := range after {
				fmt.Fprintf(&b, "%s\n\n", picture([]store.Shot{s}, 0))
			}
		}
		for _, s := range append(append([]store.Shot(nil), before...), after...) {
			if s.Caption != "" {
				fmt.Fprintf(&b, "- %s, %s: %s\n", s.Name, s.State, plain(s.Caption))
			}
		}
		b.WriteString("\n")
	}
	if run.Data != "" {
		fmt.Fprintf(&b, "They show %s, in the sandbox; never production.\n\n", plain(strings.TrimRight(run.Data, ".")))
	}
	return b.String()
}

// picture is the i-th screenshot as Markdown, or nothing.
func picture(ss []store.Shot, i int) string {
	if i >= len(ss) {
		return ""
	}
	name := strings.NewReplacer("[", "(", "]", ")", "|", "/").Replace(ss[i].Name)
	if ss[i].URL == "" {
		// The forge took no file from Graft: the picture passed its checks
		// and is named, not shown.
		return "`" + code(name) + "`"
	}
	return fmt.Sprintf("![%s](%s)", name, ss[i].URL)
}

// failureText says how a run stopped, as the ticket and the next attempt
// read it.
func failureText(f *store.Failure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#### Why it stopped\n\n%s.\n\n", plain(sentence(f.Cause)))
	if f.Where != "" {
		fmt.Fprintf(&b, "- **Where:** %s\n", plain(f.Where))
	}
	for _, t := range f.Tried {
		fmt.Fprintf(&b, "- **Tried:** %s\n", plain(t))
	}
	for _, s := range f.Stuck {
		fmt.Fprintf(&b, "- **Its agent said:** %s\n", plain(s))
	}
	b.WriteString("\n")
	if f.Detail != "" {
		fmt.Fprintf(&b, "<details><summary>The end of what it reported</summary>\n\n```\n%s\n```\n</details>\n\n", strings.ReplaceAll(f.Detail, "```", "'''"))
	}
	return b.String()
}

// stepsText is every step of the run, newest last, folded, each by its time
// since the run started.
func stepsText(run *store.Run) string {
	if len(run.Steps) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary>Steps (%d)</summary>\n\n", len(run.Steps))
	steps := run.Steps
	if len(steps) > shownSteps {
		fmt.Fprintf(&b, "%d earlier steps are on Graft's page.\n\n", len(steps)-shownSteps)
		steps = steps[len(steps)-shownSteps:]
	}
	for _, s := range steps {
		who := "Graft"
		if s.By == store.ByAgent {
			who = "agent"
		}
		text := plain(s.Text)
		switch {
		case s.Kind == store.StepCommit && s.SHA != "":
			text = "`" + shortSHA(s.SHA) + "` " + text
		case s.Kind == store.StepFound:
			text = "Found: " + text
		case s.Kind == store.StepStuck:
			text = "Stuck: " + text
		}
		if s.OK != nil {
			text = map[bool]string{true: "passed: ", false: "failed: "}[*s.OK] + text
		}
		if s.URL != "" {
			text += fmt.Sprintf(" ([link](%s))", s.URL)
		}
		fmt.Fprintf(&b, "- `+%s` %s: %s\n", since(s.At.Sub(run.Started)), who, text)
		if s.Detail != "" {
			fmt.Fprintf(&b, "  %s\n", plain(clip(s.Detail, detailMax)))
		}
	}
	b.WriteString("\n</details>\n")
	return b.String()
}

// since is a span as m:ss.
func since(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// mention is a name a forge would notify.
var mention = regexp.MustCompile(`(^|[^\w` + "`" + `])(@[\w.-]+)`)

// plain is text an agent or a person wrote, made safe for one line of a
// comment: one line, no markup that runs as HTML, and no name a forge would
// notify or cross-link by.
func plain(s string) string {
	s = clip(line(s), stepMax)
	s = strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s)
	return mention.ReplaceAllString(s, "$1`$2`")
}

// code is text for inside a code span.
func code(s string) string { return strings.ReplaceAll(clip(line(s), stepMax), "`", "'") }

// redacted is text with what looks like a secret cut out.
func redacted(s string) string {
	if kinds, cut := sweep.Scan(s); len(kinds) > 0 {
		return cut
	}
	return s
}

// runList is a request's runs as Markdown, one line each, linking to each
// run's comment, for the catalog's record.
func runList(runs []store.RunLink) string {
	var b strings.Builder
	for _, l := range runs {
		name := runTitle(&store.Run{Kind: l.Kind, Round: l.Round, Attempt: l.Attempt})
		if l.URL != "" {
			name = fmt.Sprintf("[%s](%s)", name, l.URL)
		}
		how := map[string]string{store.RunDone: "done", store.RunFailed: "stopped", "": "running"}[l.Outcome]
		fmt.Fprintf(&b, "- %s: %s\n", name, how)
	}
	return b.String()
}
