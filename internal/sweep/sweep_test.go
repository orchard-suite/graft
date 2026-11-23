package sweep

import (
	"strings"
	"testing"
)

func TestScanFindsSecretsAndRedactsThem(t *testing.T) {
	// Each value is built here so no scanner reads this file as a leak.
	cases := map[string]string{
		"aws-access-key":      "AKIA" + strings.Repeat("A", 16),
		"github-token":        "ghp_" + strings.Repeat("a", 36),
		"gitlab-token":        "glpat-" + strings.Repeat("b", 20),
		"private-key":         "-----BEGIN RSA PRIVATE KEY-----\nMIIabc\n-----END RSA PRIVATE KEY-----",
		"jwt":                 "eyJ" + strings.Repeat("a", 12) + ".eyJ" + strings.Repeat("b", 12) + "." + strings.Repeat("c", 12),
		"slack-token":         "xoxb-" + strings.Repeat("1", 12),
		"azure-storage-key":   "AccountKey=" + strings.Repeat("Q", 44) + "==",
		"azure-shared-access": "SharedAccessKey=" + strings.Repeat("Q", 30),
		"api-key":             "sk-" + strings.Repeat("k", 30),
		"bearer-token":        "Authorization: Bearer " + strings.Repeat("z", 30),
		"credential-url":      "postgres://app:hunter22@db.example.com/orders",
		"assigned-secret":     `password = "correct-horse"`,
	}
	for kind, secret := range cases {
		kinds, redacted := Scan("It broke when I ran this:\n" + secret + "\nand then the page went blank.")
		if len(kinds) == 0 || !contains(kinds, kind) {
			t.Errorf("%s: found %v", kind, kinds)
		}
		if !strings.Contains(redacted, "[removed: ") || !strings.Contains(redacted, "the page went blank") {
			t.Errorf("%s: redacted to %q", kind, redacted)
		}
	}
}

func TestScanLeavesPlainWordsAlone(t *testing.T) {
	text := "The password reset page shows a blank token field. I tried at https://app.example.com/reset?step=2 and the secret question never loads."
	if kinds, redacted := Scan(text); len(kinds) != 0 || redacted != text {
		t.Errorf("found %v in plain words: %q", kinds, redacted)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
