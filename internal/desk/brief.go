package desk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/orchard-suite/graft/internal/store"
)

// briefText is the brief as Markdown, as the approvers read it: on the
// ticket, at the top of the plan's run in the hub, and on Graft's page.
// titles name the evidence its checks cite, by number. On a round after the
// first, answered are the notes the round answers: it opens with what
// changed since the round before, quoting each.
func briefText(br *store.Brief, titles map[int]string, round int, answered []store.Note) string {
	var b strings.Builder
	if round > 1 && (len(answered) > 0 || len(br.Changed) > 0) {
		fmt.Fprintf(&b, "## What changed since round %d\n\n", round-1)
		for _, n := range answered {
			b.WriteString(quote(n.Text))
			fmt.Fprintf(&b, "\n%s\n\n", said(n))
		}
		for _, c := range br.Changed {
			fmt.Fprintf(&b, "- %s\n", line(c))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "## What was asked\n\n%s\n\n", strings.TrimSpace(br.Asked))
	fmt.Fprintf(&b, "## What we will do\n\n%s\n\n", strings.TrimSpace(br.WillDo))
	b.WriteString("## What we will not do, and why\n\n")
	if len(br.WillNot) == 0 {
		b.WriteString("Nothing that was asked for is left out.\n")
	}
	for _, w := range br.WillNot {
		fmt.Fprintf(&b, "- **%s** %s\n", line(w.What), line(w.Why))
	}
	b.WriteString("\n## Judgment calls for you\n\n")
	if len(br.Calls) == 0 {
		b.WriteString("None.\n")
	}
	for i, c := range br.Calls {
		fmt.Fprintf(&b, "%d. %s\n   *Recommended:* %s\n", i+1, line(c.Question), line(c.Recommendation))
	}
	b.WriteString("\n## What we checked\n\n")
	if len(br.Checked) == 0 {
		b.WriteString("The request states nothing that needed checking.\n")
	}
	for _, c := range br.Checked {
		fmt.Fprintf(&b, "- %s **%s.** %s%s\n", line(c.Claim), found(c.Found), line(c.Detail), cited(c.Evidence, titles))
	}
	fmt.Fprintf(&b, "\n## Fit and risk\n\n%s\n\n%s\n\n", strings.TrimSpace(br.Fit), strings.TrimSpace(br.Risk))
	fmt.Fprintf(&b, "## Recommendation\n\n**%s.** %s\n", decision(br.Recommendation.Decision), line(br.Recommendation.Why))
	return b.String()
}

// buildText is the build plan as Markdown, as the builder reads it.
func buildText(p *store.Build) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Kind of change.** %s\n\n**What the build solves.** %s\n\n**Steps.**\n\n", kindWords(p.Kind), strings.TrimSpace(p.Understood))
	for i, s := range p.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, line(s))
	}
	b.WriteString("\n**Done when.**\n\n")
	for _, a := range p.Acceptance {
		fmt.Fprintf(&b, "- %s\n", line(a))
	}
	fmt.Fprintf(&b, "\n**Tests that fail first.** %s\n\n", strings.TrimSpace(p.Tests))
	if len(p.Constraints) > 0 {
		b.WriteString("**Constraints.**\n\n")
		for _, c := range p.Constraints {
			fmt.Fprintf(&b, "- %s\n", line(c))
		}
		b.WriteString("\n")
	}
	b.WriteString("**Files it changes.**\n\n")
	for _, c := range p.Changes {
		fmt.Fprintf(&b, "- `%s`: %s\n", c.Path, line(c.What))
	}
	b.WriteString("\n**What it leaves alone.**\n\n")
	if len(p.Untouched) == 0 {
		b.WriteString("- Everything not listed above.\n")
	}
	for _, u := range p.Untouched {
		fmt.Fprintf(&b, "- %s\n", line(u))
	}
	if p.UI != nil {
		b.WriteString("\n**Screens it changes.**\n\n")
		for _, s := range p.UI.Screens {
			fmt.Fprintf(&b, "- %s\n", line(s))
		}
		fmt.Fprintf(&b, "\n**How they are shown.** %s\n", line(p.UI.Shots))
	}
	fmt.Fprintf(&b, "\n**How big it is.** About %d lines in %d files.\n\n", p.Size.Lines, p.Size.Files)
	tiers := "None beyond the lane's."
	if len(p.Tiers) > 0 {
		tiers = strings.Join(p.Tiers, ", ")
	}
	fmt.Fprintf(&b, "**Tool tiers it needs.** %s\n", tiers)
	if len(p.Protected) > 0 {
		fmt.Fprintf(&b, "\n**Protected paths it changes.** `%s`\n", strings.Join(p.Protected, "`, `"))
	}
	return b.String()
}

// briefed says the plan has a brief. A plan kept from before Graft wrote
// briefs has its build plan alone.
func briefed(p *store.Plan) bool { return strings.TrimSpace(p.Brief.Asked) != "" }

// planBrief is the request's brief for round as Markdown, with what each
// check cites named, or the build plan of a plan that has no brief.
func (d *Desk) planBrief(ctx context.Context, r *store.Request, round int) string {
	if !briefed(r.Plan) {
		return "This plan was written before Graft wrote briefs. Its build plan is below.\n"
	}
	return briefText(&r.Plan.Brief, d.evidenceTitles(ctx, r), round, answeredBy(r, round))
}

// evidenceTitles names each piece of the request's evidence by its number.
func (d *Desk) evidenceTitles(ctx context.Context, r *store.Request) map[int]string {
	evidence, _ := d.store.Evidence(ctx, r.ID)
	titles := map[int]string{}
	for _, e := range evidence {
		titles[e.N] = e.Title
	}
	return titles
}

// quote is text as a Markdown block quote.
func quote(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// said names who said a note, and where.
func said(n store.Note) string {
	where := "on the ticket"
	if n.From == store.FromHub {
		where = "sending the plan back in the hub"
	}
	if n.URL != "" {
		where = fmt.Sprintf("[%s](%s)", where, n.URL)
	}
	if len(n.Files) > 0 {
		return fmt.Sprintf("Said by %s, %s, with %s.", n.By, where, strings.Join(n.Files, ", "))
	}
	return fmt.Sprintf("Said by %s, %s.", n.By, where)
}

// line is text kept to one line of a list.
func line(s string) string { return strings.Join(strings.Fields(s), " ") }

func found(f string) string {
	switch f {
	case store.CheckTrue:
		return "True"
	case store.CheckFalse:
		return "Not so"
	case store.CheckPartly:
		return "Partly"
	}
	return "Could not check"
}

func decision(d string) string {
	switch d {
	case store.Approve:
		return "Approve"
	case store.ApproveWithChanges:
		return "Approve with changes"
	case store.Decline:
		return "Decline"
	}
	return d
}

func kindWords(k string) string {
	if k == store.KindFix {
		return "a fix: the system will do what it was already meant to do."
	}
	return "a feature: the system will do something it does not do now."
}

// cited names the evidence a check cites, as "(evidence 2: title)".
func cited(ns []int, titles map[int]string) string {
	if len(ns) == 0 {
		return ""
	}
	var out []string
	for _, n := range ns {
		if t := titles[n]; t != "" {
			out = append(out, fmt.Sprintf("%d: %s", n, line(t)))
		} else {
			out = append(out, fmt.Sprint(n))
		}
	}
	return " (evidence " + strings.Join(out, "; ") + ")"
}

// verdictText is a verdict as the investigator writes one, for a round that
// revises it.
func verdictText(v *store.Verdict) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw) + "\n"
}
