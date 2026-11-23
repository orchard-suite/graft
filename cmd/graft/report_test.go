package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orchard-suite/graft/internal/desk"
)

// The agent's report is files beside its clone, in the shapes Graft reads
// strictly, and the command writes nothing else and calls nothing.
func TestTheReportIsWrittenBesideTheCloneInTheShapesGraftReads(t *testing.T) {
	home := t.TempDir()
	clone := filepath.Join(home, "repo")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, desk.HandleFile), []byte(`{"url":"http://127.0.0.1:1","secret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "form-after.png"), []byte("\x89PNG\r\n\x1a\nafter"), 0o644); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(clone); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"step", "Reading", "the", "form"},
		{"test", "npm test -- request-form", "--passed", "12 tests"},
		{"stuck", "The e2e server will not start"},
		{"shot", "form-after.png", "--screen", "Request form", "--after", "A unit beside the quantity"},
		{"proof", "1", "--shot", "form-after.png"},
		{"proof", "2", "--test", "RequestFormComponent saves the unit"},
		{"data", "the e2e fixtures"},
	} {
		if err := reportCommand(args, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if err := reportCommand([]string{"test", "make check"}, io.Discard); err == nil {
		t.Error("a test with no result was taken")
	}
	report := filepath.Join(home, desk.ReportDir)
	raw, err := os.ReadFile(filepath.Join(report, desk.ProgressFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("progress: %q", raw)
	}
	for _, l := range lines {
		var e desk.Entry
		dec := json.NewDecoder(strings.NewReader(l))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
	if !strings.Contains(lines[1], `"ok":true`) || !strings.Contains(lines[0], `"text":"Reading the form"`) {
		t.Errorf("progress: %q", raw)
	}
	raw, err = os.ReadFile(filepath.Join(report, desk.ShotsDir, desk.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var m desk.Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	if len(m.Shots) != 1 || m.Shots[0].File != "form-after.png" || m.Shots[0].State != "after" || m.Shots[0].Screen != "Request form" ||
		len(m.Proofs) != 2 || m.Proofs[1].Test != "RequestFormComponent saves the unit" || m.Data != "the e2e fixtures" {
		t.Errorf("manifest: %+v", m)
	}
	if got, _ := os.ReadFile(filepath.Join(report, desk.ShotsDir, "form-after.png")); string(got) != "\x89PNG\r\n\x1a\nafter" {
		t.Errorf("the shot was not copied into the folder: %q", got)
	}
}

// The builder says it is finished, or what is left, in one file Graft
// reads strictly. Anything left makes the work unfinished, whatever was
// said before.
func TestTheBuilderSaysItIsFinishedOrWhatIsLeftInTheFinishFile(t *testing.T) {
	home := t.TempDir()
	clone := filepath.Join(home, "repo")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, desk.HandleFile), []byte(`{"url":"http://127.0.0.1:1","secret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(clone); err != nil {
		t.Fatal(err)
	}
	read := func() desk.Finish {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(home, desk.ReportDir, desk.FinishFile))
		if err != nil {
			t.Fatal(err)
		}
		var f desk.Finish
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if err := reportCommand([]string{"done", "The order request form"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f := read(); !f.Done || f.Summary != "The order request form" || len(f.Left) != 0 {
		t.Errorf("done: %+v", f)
	}
	if err := reportCommand([]string{"left", "The Forms seed files are not regenerated"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f := read(); f.Done || len(f.Left) != 1 || f.Left[0] != "The Forms seed files are not regenerated" {
		t.Errorf("left after done: %+v", f)
	}
	if err := reportCommand([]string{"done", "All of it"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f := read(); f.Done {
		t.Errorf("done again with something left must stay unfinished: %+v", f)
	}
	if err := reportCommand([]string{"left"}, io.Discard); err == nil {
		t.Error("an empty left was taken")
	}
}
