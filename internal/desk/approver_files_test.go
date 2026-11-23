package desk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orchard-suite/platformkit/v3/orrery"

	"github.com/orchard-suite/graft/internal/attach"
	"github.com/orchard-suite/graft/internal/forge"
	"github.com/orchard-suite/graft/internal/store"
)

var logo = append([]byte("\x89PNG\r\n\x1a\n"), []byte(" the company logo")...)

// lastComment is what Graft last said on the ticket.
func (fx *fixture) lastComment() string {
	fx.t.Helper()
	if len(fx.forge.comments) == 0 {
		fx.t.Fatal("Graft said nothing on the ticket")
	}
	return fx.forge.comments[len(fx.forge.comments)-1]
}

// listen reads the ticket's comments now, and reads the request back.
func (fx *fixture) listen(r *store.Request) *store.Request {
	fx.t.Helper()
	if err := fx.d.Listen(context.Background(), r.ID); err != nil {
		fx.t.Fatal(err)
	}
	got, err := fx.st.Get(context.Background(), r.ID)
	if err != nil {
		fx.t.Fatal(err)
	}
	return got
}

func TestAnApproversFileOnTheTicketIsPlannedWithAndBuiltFrom(t *testing.T) {
	readers(t)
	fx, r := waitingFeature(t)
	fx.findings(feature(), featurePlan("a.txt", "logo.png"))
	fx.forge.files = map[string][]byte{"aa/logo.png": logo}
	fx.forge.sayWith(r.Ticket.Number, "lead", "Use our logo on the page: ![logo](/uploads/aa/logo.png)", forge.Upload{Ref: "aa/logo.png", Name: "logo.png"})
	r = fx.listen(r)
	if len(r.Incoming) != 1 || len(r.Attachments) != 0 || len(r.Thread) != 1 || strings.Join(r.Thread[0].Files, ",") != "logo.png" {
		t.Fatalf("incoming %+v attachments %+v thread %+v", r.Incoming, r.Attachments, r.Thread)
	}
	if f := r.Incoming[0]; f.From != attach.FromApprover || f.By != "lead@example.com" || f.Round != 1 || f.ContentType != attach.PNG {
		t.Errorf("the file is not marked as the approver's: %+v", f)
	}
	var planned bool
	inner := fx.rt.investigate
	fx.rt.investigate = func(dir, prompt string) error {
		data, err := os.ReadFile(filepath.Join(dir, inDir, "1-logo.png"))
		planned = err == nil && string(data) == string(logo)
		return inner(dir, prompt)
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || len(r.Incoming) != 0 || len(r.Attachments) != 1 || !r.Attachments[0].Approvers() || !planned {
		t.Fatalf("state %s round %d incoming %+v attachments %+v planned %v", r.State, r.PlanRound, r.Incoming, r.Attachments, planned)
	}
	sent := "a file lead@example.com sent in round 1 for the plan to use"
	if prompt := fx.rt.prompts[1]; !strings.Contains(prompt, "- `.graft-in/1-logo.png`: \"logo.png\", "+sent+".") ||
		strings.Contains(prompt, "the person's own picture") || !strings.Contains(prompt, "Use our logo on the page") || !strings.Contains(prompt, "Files it sent: logo.png.") {
		t.Errorf("the planner's brief:\n%s", prompt)
	}
	link := fmt.Sprintf("[logo.png](https://graft.example.com/api/requests/%s/attachments/%s) (image/png, %d bytes), from lead@example.com in round 1", r.ID, r.Attachments[0].ID, len(logo))
	for where, text := range map[string]string{"the run": fx.hub.asks[1].Inputs["files"].(string), "the ticket": fx.forge.bodies[r.Ticket.Number], "the round's comment": fx.lastComment()} {
		if !strings.Contains(text, link) {
			t.Errorf("%s does not show the approver's file:\n%s", where, text)
		}
	}
	var prompt string
	fx.rt.build = func(dir, p string) error {
		prompt = p
		data, err := os.ReadFile(filepath.Join(filepath.Dir(dir), inBeside, "1-logo.png"))
		if err != nil {
			return fmt.Errorf("the approver's file is not beside the clone: %w", err)
		}
		write(t, dir, "logo.png", string(data))
		return fx.commitTwo(dir)
	}
	fx.hub.decide(r.Run, orrery.RunQueued, "lead@example.com", "")
	r = fx.poll(r)
	if r.State != store.StateReleased || fx.remoteFile("logo.png") != string(logo) {
		t.Fatalf("state %s logo %q", r.State, fx.remoteFile("logo.png"))
	}
	if !strings.Contains(prompt, "- `../in/1-logo.png`: \"logo.png\", "+sent+".") || !strings.Contains(prompt, "never commit one unless the plan says to use it") {
		t.Errorf("the builder's brief:\n%s", prompt)
	}
}

func TestOnlyAnApproversCommentHasItsFilesRead(t *testing.T) {
	fx, r := waitingFeature(t)
	fx.forge.files = map[string][]byte{"aa/logo.png": logo}
	fx.forge.sayWith(r.Ticket.Number, "stranger", "Here: ![logo](/uploads/aa/logo.png)", forge.Upload{Ref: "aa/logo.png", Name: "logo.png"})
	r = fx.listen(r)
	if len(fx.forge.fetched) != 0 || len(r.Incoming) != 0 || len(r.Thread) != 0 {
		t.Fatalf("a stranger's file was read: fetched %v incoming %+v", fx.forge.fetched, r.Incoming)
	}
}

func TestAFileOverALimitOrOfAnotherKindIsLeftOutAndTheRoundGoesOn(t *testing.T) {
	readers(t)
	fx, r := waitingFeature(t)
	huge := append(append([]byte{}, logo...), make([]byte, attach.MaxFile)...)
	fx.forge.files = map[string][]byte{"aa/logo.png": logo, "bb/logo.svg": []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), "cc/huge.png": huge}
	fx.forge.sayWith(r.Ticket.Number, "lead", "Use one of these.",
		forge.Upload{Ref: "bb/logo.svg", Name: "logo.svg"}, forge.Upload{Ref: "cc/huge.png", Name: "huge.png"}, forge.Upload{Ref: "dd/gone.png", Name: "gone.png"},
		forge.Upload{Ref: "https://github.com/user-attachments/assets/1f2e", Name: "a file"}, forge.Upload{Ref: "aa/logo.png", Name: "logo.png"})
	r = fx.listen(r)
	if len(r.Incoming) != 1 || r.Incoming[0].Name != "logo.png" || strings.Join(r.Thread[0].Files, ",") != "logo.png" {
		t.Fatalf("incoming %+v thread %+v", r.Incoming, r.Thread)
	}
	said := fx.lastComment()
	for _, why := range []string{"logo.svg is not a picture", "huge.png is larger than 10 MB", "gone.png could not be read from the forge", "a file: this forge does not let Graft read a file attached to a comment"} {
		if !strings.Contains(said, why) {
			t.Errorf("the ticket is not told %q:\n%s", why, said)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(fx.d.cfg.DataDir, "attachments", r.ID))
	if len(entries) != 1 {
		t.Errorf("%d files kept on disk, want 1", len(entries))
	}
	r = fx.work(r)
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || len(r.Attachments) != 1 {
		t.Fatalf("the round did not go on: state %s round %d attachments %+v", r.State, r.PlanRound, r.Attachments)
	}
}

func TestASecretInAnApproversFileDropsThatFileAlone(t *testing.T) {
	readers(t)
	fx, r := waitingFeature(t)
	token := "glpat-" + strings.Repeat("c", 24)
	fx.forge.files = map[string][]byte{"aa/logo.png": logo, "ee/leak.png": append(append([]byte{}, logo...), []byte(" token "+token)...)}
	fx.forge.sayWith(r.Ticket.Number, "lead", "Two pictures.", forge.Upload{Ref: "aa/logo.png", Name: "logo.png"}, forge.Upload{Ref: "ee/leak.png", Name: "leak.png"})
	r = fx.work(fx.listen(r))
	if r.State != store.StateAwaitingPlan || r.PlanRound != 2 || len(r.Attachments) != 1 || r.Attachments[0].Name != "logo.png" || len(r.Incoming) != 0 || len(r.Secrets) != 0 {
		t.Fatalf("state %s round %d attachments %+v incoming %+v secrets %v", r.State, r.PlanRound, r.Attachments, r.Incoming, r.Secrets)
	}
	entries, _ := os.ReadDir(filepath.Join(fx.d.cfg.DataDir, "attachments", r.ID))
	if len(entries) != 1 {
		t.Errorf("%d files on disk, want the logo alone", len(entries))
	}
	var told bool
	for _, c := range fx.forge.comments {
		if strings.Contains(c, token) {
			t.Errorf("the ticket quotes the secret:\n%s", c)
		}
		told = told || (strings.Contains(c, "leak.png") && strings.Contains(c, "gitlab-token"))
	}
	if !told {
		t.Errorf("the ticket is not told the file was deleted: %q", fx.forge.comments)
	}
	if strings.Contains(fx.rt.prompts[1], "leak.png") {
		t.Error("the planner was shown the file that held a secret")
	}
}

func TestASecretInAnApproversCommentLeavesItOutOfTheRound(t *testing.T) {
	fx, r := waitingFeature(t)
	token := "glpat-" + strings.Repeat("d", 24)
	fx.forge.files = map[string][]byte{"aa/logo.png": logo}
	fx.forge.sayWith(r.Ticket.Number, "lead", "Deploy with "+token+" and use ![logo](/uploads/aa/logo.png)", forge.Upload{Ref: "aa/logo.png", Name: "logo.png"})
	r = fx.listen(r)
	if r.State != store.StateAwaitingPlan || len(r.Thread) != 0 || len(fx.forge.fetched) != 0 || len(r.Incoming) != 0 {
		t.Fatalf("a comment with a secret was taken: state %s thread %+v fetched %v", r.State, r.Thread, fx.forge.fetched)
	}
	said := fx.lastComment()
	if strings.Contains(said, token) || !strings.Contains(said, "gitlab-token") || !strings.Contains(said, "#note_2000") {
		t.Errorf("the ticket is told:\n%s", said)
	}
	fx.forge.say(r.Ticket.Number, "lead", "Keep the old line.")
	r = fx.work(fx.listen(r))
	if r.PlanRound != 2 || len(r.Thread) != 1 || strings.Contains(fx.rt.prompts[1], token) || !strings.Contains(fx.rt.prompts[1], "Keep the old line.") {
		t.Errorf("round %d thread %+v prompt:\n%s", r.PlanRound, r.Thread, fx.rt.prompts[1])
	}
}

func TestASecretInANoteSentBackIsRemovedBeforeAnyAgentReadsIt(t *testing.T) {
	fx, r := waitingFeature(t)
	fx.hub.decide(r.Run, orrery.RunReturned, "lead@example.com", "Keep the old line; the password=hunter2hunter2 works.")
	r = fx.poll(r)
	if r.PlanRound != 2 || strings.Contains(fx.rt.prompts[1], "hunter2") || !strings.Contains(fx.rt.prompts[1], "Keep the old line; the [removed: assigned-secret]") {
		t.Fatalf("round %d prompt:\n%s", r.PlanRound, fx.rt.prompts[1])
	}
	for _, c := range fx.forge.comments {
		if strings.Contains(c, "hunter2") {
			t.Errorf("the ticket quotes the secret:\n%s", c)
		}
	}
	if strings.Contains(r.DecisionNote, "hunter2") || !strings.Contains(fx.lastComment()+strings.Join(fx.forge.comments, "\n"), "assigned-secret") {
		t.Errorf("note %q comments %q", r.DecisionNote, fx.forge.comments)
	}
}
