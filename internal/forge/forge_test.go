package forge

import (
	"context"
	"strings"
	"testing"
)

func TestCredentialAnswersOnlyItsOwnForge(t *testing.T) {
	tokens := func(_ context.Context, repo string) (string, error) { return "tok-" + repo, nil }
	ask := func(in string) string {
		var out strings.Builder
		if err := Credential(context.Background(), "https://gitlab.example.com", "oauth2", tokens, strings.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := ask("protocol=https\nhost=gitlab.example.com\npath=acme/group/api.git\n"); got != "username=oauth2\npassword=tok-acme/group/api\n" {
		t.Errorf("own forge: %q", got)
	}
	if got := ask("protocol=https\nhost=evil.example.com\npath=acme/api.git\n"); got != "" {
		t.Errorf("another host got an answer: %q", got)
	}
	if got := ask("protocol=http\nhost=gitlab.example.com\npath=acme/api.git\n"); got != "" {
		t.Errorf("another protocol got an answer: %q", got)
	}
	var out strings.Builder
	if err := Credential(context.Background(), "https://gitlab.example.com", "oauth2", tokens, strings.NewReader("protocol=https\nhost=gitlab.example.com\n"), &out); err == nil {
		t.Error("a request with no path must fail, not get a token for nothing")
	}
}

func TestOnlySuccessIsGreen(t *testing.T) {
	for conclusion, green := range map[string]bool{"success": true, "skipped": false, "neutral": false, "cancelled": false, "manual": false, "failure": false, "timed_out": false} {
		if got := (Run{Status: "completed", Conclusion: conclusion}).Green(); got != green {
			t.Errorf("%s green = %v", conclusion, got)
		}
	}
	if (Run{Status: "running", Conclusion: "success"}).Green() {
		t.Error("a run still going is not green")
	}
}
