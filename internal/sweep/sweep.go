// Package sweep looks for secrets in what a person sent, before any agent
// reads it. A hit names the kind of secret and never its value.
package sweep

import (
	"regexp"
	"sort"
)

type pattern struct {
	kind string
	re   *regexp.Regexp
}

var patterns = []pattern{
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})\b`)},
	{"gitlab-token", regexp.MustCompile(`\bgl(?:pat|ptt|rt|dt)-[A-Za-z0-9_\-]{20,}`)},
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	{"slack-token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}`)},
	{"azure-storage-key", regexp.MustCompile(`(?i)AccountKey=[A-Za-z0-9+/]{40,}={0,2}`)},
	{"azure-shared-access", regexp.MustCompile(`(?i)SharedAccessKey=[A-Za-z0-9+/]{20,}={0,2}`)},
	{"api-key", regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{24,}`)},
	{"bearer-token", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9_\-\.=+/]{20,}`)},
	{"credential-url", regexp.MustCompile(`\b[a-z][a-z0-9+.\-]*://[^\s/:@]+:[^\s/@]{3,}@`)},
	{"assigned-secret", regexp.MustCompile(`(?i)\b(?:password|passwd|pwd|secret|token|api[_-]?key|client[_-]?secret)\b["']?\s*[:=]\s*["']?[^\s"']{8,}`)},
}

// Scan returns the kinds of secret found in text, sorted, and the text with
// each one replaced by a mark that names its kind.
func Scan(text string) (kinds []string, redacted string) {
	seen := map[string]bool{}
	redacted = text
	for _, p := range patterns {
		if !p.re.MatchString(redacted) {
			continue
		}
		seen[p.kind] = true
		redacted = p.re.ReplaceAllString(redacted, "[removed: "+p.kind+"]")
	}
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds, redacted
}
