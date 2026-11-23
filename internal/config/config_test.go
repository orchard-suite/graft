package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const file = `
program: acme
forge: { kind: gitlab, url: https://gitlab.example.com }
committer: { email: graft@example.com }
repositories:
- { path: acme/api, entity: "repository:acme-api" }
approvers:
- { path: teams, values: [platform] }
lanes:
- id: developer
  members: [{ path: teams, values: [builders] }]
`

// set gives the environment a whole, working setup.
func set(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "program.yaml")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"GRAFT_PROGRAM_FILE": path, "GRAFT_AUTH_MODE": "proxy", "GRAFT_ORRERY_URL": "http://hub.example.com/",
		"GRAFT_ORRERY_TOKEN": "a", "ORRERY_PUSH_TOKEN": "b", "GRAFT_FORGE_TOKEN_FILE": "/run/secrets/forge",
		"GRAFT_GITHUB_APP_ID": "", "GRAFT_GITHUB_APP_KEY_FILE": "", "GRAFT_BUILD_TIMEOUT": "", "GRAFT_SANDBOX": "", "GRAFT_SANDBOX_POD_FILE": "",
		"GRAFT_HOOK_SECRET_FILE": "", "GRAFT_HOOK_LISTEN": "",
	} {
		t.Setenv(k, v)
	}
}

func TestFromEnv(t *testing.T) {
	set(t)
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.Program.Name != "acme" || c.OrreryURL != "http://hub.example.com" || c.HandleURL != "http://127.0.0.1:8082" || c.BuildRounds != 3 {
		t.Errorf("config: %+v", c)
	}
}

func TestFromEnvRefusesAHalfSetup(t *testing.T) {
	for name, env := range map[string][2]string{
		"no program file":                {"GRAFT_PROGRAM_FILE", ""},
		"no hub":                         {"GRAFT_ORRERY_URL", ""},
		"no source token":                {"ORRERY_PUSH_TOKEN", ""},
		"no forge credential":            {"GRAFT_FORGE_TOKEN_FILE", ""},
		"no word on who signs people in": {"GRAFT_AUTH_MODE", ""},
		"a duration that is not one":     {"GRAFT_BUILD_TIMEOUT", "soon"},
		"a sandbox it does not know":     {"GRAFT_SANDBOX", "docker"},
		"pods with no pod to make":       {"GRAFT_SANDBOX", "kubernetes"},
	} {
		set(t)
		t.Setenv(env[0], env[1])
		if _, err := FromEnv(); err == nil {
			t.Errorf("%s: started", name)
		}
	}
	set(t)
	t.Setenv("GRAFT_GITHUB_APP_ID", "1")
	t.Setenv("GRAFT_GITHUB_APP_KEY_FILE", "/run/secrets/app")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("two forge credentials: %v", err)
	}
	t.Setenv("GRAFT_FORGE_TOKEN_FILE", "")
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "github only") {
		t.Errorf("a GitHub App on gitlab: %v", err)
	}
}

func TestWorkspaceNeedsNoRunnerSettings(t *testing.T) {
	t.Setenv("GRAFT_PROGRAM_FILE", "")
	w := WorkspaceFromEnv()
	if w.WorkDir == "" || w.Listen == "" {
		t.Errorf("workspace: %+v", w)
	}
}

func TestTheForgesPortIsOnOnlyWithItsSecretAndForGitLab(t *testing.T) {
	set(t)
	c, err := FromEnv()
	if err != nil || c.HookSecretFile != "" || c.HookListen != ":8083" {
		t.Fatalf("no secret, no forge port: %+v %v", c, err)
	}
	t.Setenv("GRAFT_HOOK_SECRET_FILE", "/run/secrets/hook")
	if c, err = FromEnv(); err != nil || c.HookSecretFile != "/run/secrets/hook" {
		t.Fatalf("a GitLab program with the secret: %v", err)
	}
	github := strings.Replace(file, "forge: { kind: gitlab, url: https://gitlab.example.com }", "forge: { kind: github }", 1)
	path := filepath.Join(t.TempDir(), "program.yaml")
	if err := os.WriteFile(path, []byte(github), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRAFT_PROGRAM_FILE", path)
	if _, err := FromEnv(); err == nil || !strings.Contains(err.Error(), "GitLab's webhooks") {
		t.Errorf("a GitHub program with the hook's secret: %v", err)
	}
}
