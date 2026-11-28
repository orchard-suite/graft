// Package config reads Graft's settings from the environment: where it
// listens, where it keeps things, and the addresses and credentials of what
// it talks to. How it works (repositories, lanes, limits) is the program
// file's, not the environment's.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/orchard-suite/graft/internal/auth"
	"github.com/orchard-suite/graft/internal/program"
)

// Where an agent session runs.
const (
	SandboxShared     = "shared"
	SandboxKubernetes = "kubernetes"
)

type Config struct {
	// Program is the program file, read once at start.
	Program *program.Program

	// Listen is the page's port, behind the front door. APIListen is the
	// token port, for intake apps. HandleListen is the loopback port the
	// agent's CLI calls, and HandleURL is how the CLI reaches it.
	Listen       string
	APIListen    string
	HandleListen string
	HandleURL    string
	// HookListen is the forge's port, where the forge says an issue was
	// commented on. HookSecretFile holds the secret the forge must send;
	// empty serves no forge port.
	HookListen     string
	HookSecretFile string
	// AuthMode says how the page's person is known.
	AuthMode auth.Mode
	// IntakeTokenDir holds one file per intake app, named for the app,
	// whose content is the app's token. Empty serves no token port.
	IntakeTokenDir string

	// DataDir holds the database and the logs. RepoDir holds the runner's
	// own clones, the only place it runs git. WorkDir is the agent's: the
	// runner reaches it only through the workspace at WorkspaceURL.
	DataDir      string
	RepoDir      string
	WorkDir      string
	WorkspaceURL string

	// Sandbox says where an agent session runs. "shared": the engine and
	// the workspace beside Graft in one pod, one directory per session.
	// "kubernetes": a pod of its own per session, made from SandboxPodFile
	// in SandboxNamespace, reached at SandboxPort, and deleted when the
	// session ends.
	Sandbox          string
	SandboxPodFile   string
	SandboxNamespace string
	SandboxPort      int
	SandboxStartWait time.Duration

	// The forge credential: a GitHub App, or one token in a file.
	GitHubAppID      string
	GitHubAppKeyFile string
	ForgeTokenFile   string

	// SigningKeyFile is the SSH private key the runner signs landing proofs
	// with, a mounted file. Empty, a repository whose release config names
	// a check does not land.
	SigningKeyFile string

	ScreenModel        string
	InvestigateModel   string
	BuildModel         string
	InvestigateTurns   int
	BuildTurns         int
	InvestigateTimeout time.Duration
	BuildTimeout       time.Duration
	// BuildRounds is how many times a build may go back to its agent.
	BuildRounds int
	// PipelineWait is how long a landed commit's pipeline may take.
	PipelineWait time.Duration

	// OrreryURL is the catalog hub's machine port. OrreryToken is a machine
	// access token that asks, lends, reports, and reads targets.
	// OrreryPushToken is a source token that declares actions and writes
	// entities. OrreryPublicURL is where a person opens the hub.
	OrreryURL       string
	OrreryToken     string
	OrreryPushToken string
	OrreryPublicURL string

	// DispatchURL is the incident desk's API port and DispatchToken a token
	// it issued. Either empty, Graft checks no incident and tells nobody.
	DispatchURL   string
	DispatchToken string

	// AnnalsURL is the API of Annals, the audit log, and AnnalsToken its token for
	// Graft. Either empty, no event is posted.
	AnnalsURL   string
	AnnalsToken string

	// OTLPEndpoint is the OTLP/HTTP receiver the requests' metrics go to;
	// empty sends none. MetricsEvery is how often.
	OTLPEndpoint string
	MetricsEvery time.Duration

	// PublicURL is where a person opens Graft.
	PublicURL string
}

// FromEnv reads every setting and the program file. It fails on anything
// Graft cannot work without, so a half-configured Graft never starts.
func FromEnv() (Config, error) {
	c := Config{
		Listen:           env("GRAFT_LISTEN", ":8080"),
		APIListen:        env("GRAFT_API_LISTEN", ":8081"),
		HandleListen:     env("GRAFT_HANDLE_LISTEN", "127.0.0.1:8082"),
		IntakeTokenDir:   os.Getenv("GRAFT_INTAKE_TOKEN_DIR"),
		HookListen:       env("GRAFT_HOOK_LISTEN", ":8083"),
		HookSecretFile:   os.Getenv("GRAFT_HOOK_SECRET_FILE"),
		DataDir:          env("GRAFT_DATA_DIR", "data"),
		RepoDir:          env("GRAFT_REPO_DIR", "repos"),
		WorkDir:          env("GRAFT_WORK_DIR", "work"),
		WorkspaceURL:     env("GRAFT_WORKSPACE_URL", "http://127.0.0.1:8090"),
		Sandbox:          env("GRAFT_SANDBOX", SandboxShared),
		SandboxPodFile:   os.Getenv("GRAFT_SANDBOX_POD_FILE"),
		SandboxNamespace: os.Getenv("GRAFT_SANDBOX_NAMESPACE"),
		GitHubAppID:      os.Getenv("GRAFT_GITHUB_APP_ID"),
		GitHubAppKeyFile: os.Getenv("GRAFT_GITHUB_APP_KEY_FILE"),
		ForgeTokenFile:   os.Getenv("GRAFT_FORGE_TOKEN_FILE"),
		SigningKeyFile:   os.Getenv("GRAFT_SIGNING_KEY_FILE"),
		ScreenModel:      env("GRAFT_SCREEN_MODEL", "claude-haiku-5-5"),
		InvestigateModel: env("GRAFT_INVESTIGATE_MODEL", "claude-sonnet-5-5"),
		BuildModel:       env("GRAFT_BUILD_MODEL", "claude-sonnet-5-5"),
		OrreryURL:        strings.TrimRight(os.Getenv("GRAFT_ORRERY_URL"), "/"),
		OrreryToken:      os.Getenv("GRAFT_ORRERY_TOKEN"),
		OrreryPushToken:  os.Getenv("ORRERY_PUSH_TOKEN"),
		OrreryPublicURL:  strings.TrimRight(os.Getenv("GRAFT_ORRERY_PUBLIC_URL"), "/"),
		DispatchURL:      strings.TrimRight(os.Getenv("GRAFT_DISPATCH_URL"), "/"),
		DispatchToken:    os.Getenv("GRAFT_DISPATCH_TOKEN"),
		AnnalsURL:        os.Getenv("GRAFT_ANNALS_URL"),
		AnnalsToken:      os.Getenv("GRAFT_ANNALS_TOKEN"),
		OTLPEndpoint:     os.Getenv("GRAFT_OTLP_ENDPOINT"),
		PublicURL:        strings.TrimRight(os.Getenv("GRAFT_PUBLIC_URL"), "/"),
	}
	c.HandleURL = env("GRAFT_HANDLE_URL", "http://"+c.HandleListen)
	var err error
	if c.AuthMode, err = auth.ParseMode(os.Getenv("GRAFT_AUTH_MODE")); err != nil {
		return c, err
	}
	if c.InvestigateTimeout, err = durationEnv("GRAFT_INVESTIGATE_TIMEOUT", 45*time.Minute); err != nil {
		return c, err
	}
	if c.BuildTimeout, err = durationEnv("GRAFT_BUILD_TIMEOUT", 2*time.Hour); err != nil {
		return c, err
	}
	if c.PipelineWait, err = durationEnv("GRAFT_PIPELINE_WAIT", 30*time.Minute); err != nil {
		return c, err
	}
	if c.MetricsEvery, err = durationEnv("GRAFT_METRICS_EVERY", 30*time.Second); err != nil {
		return c, err
	}
	if c.InvestigateTurns, err = intEnv("GRAFT_INVESTIGATE_TURNS", 150); err != nil {
		return c, err
	}
	if c.BuildTurns, err = intEnv("GRAFT_BUILD_TURNS", 400); err != nil {
		return c, err
	}
	if c.BuildRounds, err = intEnv("GRAFT_BUILD_ROUNDS", 3); err != nil {
		return c, err
	}
	if c.SandboxPort, err = intEnv("GRAFT_SANDBOX_PORT", 8090); err != nil {
		return c, err
	}
	if c.SandboxStartWait, err = durationEnv("GRAFT_SANDBOX_START_WAIT", 5*time.Minute); err != nil {
		return c, err
	}
	file := os.Getenv("GRAFT_PROGRAM_FILE")
	if file == "" {
		return c, fmt.Errorf("GRAFT_PROGRAM_FILE is required: the program file's path")
	}
	if c.Program, err = program.Load(file); err != nil {
		return c, err
	}
	return c, c.check()
}

// check refuses a setup Graft cannot work in.
func (c Config) check() error {
	var missing []string
	need := func(name, value string) {
		if value == "" {
			missing = append(missing, name)
		}
	}
	need("GRAFT_ORRERY_URL", c.OrreryURL)
	need("GRAFT_ORRERY_TOKEN", c.OrreryToken)
	need("ORRERY_PUSH_TOKEN", c.OrreryPushToken)
	app := c.GitHubAppID != "" && c.GitHubAppKeyFile != ""
	switch {
	case app && c.ForgeTokenFile != "":
		return fmt.Errorf("set the GitHub App or GRAFT_FORGE_TOKEN_FILE, not both")
	case app && c.Program.Forge.Kind != "github":
		return fmt.Errorf("a GitHub App signs in to github only; the program's forge is %s: set GRAFT_FORGE_TOKEN_FILE", c.Program.Forge.Kind)
	case !app && c.ForgeTokenFile == "":
		missing = append(missing, "GRAFT_FORGE_TOKEN_FILE (or GRAFT_GITHUB_APP_ID and GRAFT_GITHUB_APP_KEY_FILE)")
	}
	if c.HookSecretFile != "" && c.Program.Forge.Kind != "gitlab" {
		return fmt.Errorf("GRAFT_HOOK_SECRET_FILE serves the forge's port, which takes GitLab's webhooks; the program's forge is %s, whose comments the sweep reads", c.Program.Forge.Kind)
	}
	if c.Program.Hours.Schedule != "" && (c.DispatchURL == "" || c.DispatchToken == "") {
		return fmt.Errorf("hours.schedule names an on-call schedule, so GRAFT_DISPATCH_URL and GRAFT_DISPATCH_TOKEN are required")
	}
	switch c.Sandbox {
	case SandboxShared:
	case SandboxKubernetes:
		need("GRAFT_SANDBOX_POD_FILE", c.SandboxPodFile)
	default:
		return fmt.Errorf("GRAFT_SANDBOX %q: want %s or %s", c.Sandbox, SandboxShared, SandboxKubernetes)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Workspace is all `graft workspace` reads. Its container holds none of
// the runner's settings, so it must start with none of them set.
type Workspace struct {
	WorkDir string
	Listen  string
	// Token, when set, is the bearer token every call must carry. A
	// sandbox pod's workspace is given one.
	Token string
}

func WorkspaceFromEnv() Workspace {
	return Workspace{
		WorkDir: env("GRAFT_WORK_DIR", "work"),
		Listen:  env("GRAFT_WORKSPACE_LISTEN", "127.0.0.1:8090"),
		Token:   os.Getenv("GRAFT_WORKSPACE_TOKEN"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func intEnv(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}
