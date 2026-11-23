// Graft: a request desk for software that already exists. A person
// reports a problem or asks for a change; Graft investigates, plans,
// builds, lands, and answers.
//
//	graft                  serve the ports and work requests
//	graft workspace        serve the agent's clones to the runner
//	graft git-credential   answer git with the forge's credential
//	graft tool ...         an agent's handle on the repository's tools
//	graft evidence ...     an agent's way to keep what it found
//	graft version
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/orchard-suite/platformkit/v3/dispatch"
	kittelemetry "github.com/orchard-suite/platformkit/v3/telemetry"

	"github.com/orchard-suite/graft/internal/annals"
	"github.com/orchard-suite/graft/internal/auth"
	"github.com/orchard-suite/graft/internal/config"
	"github.com/orchard-suite/graft/internal/desk"
	"github.com/orchard-suite/graft/internal/forge"
	"github.com/orchard-suite/graft/internal/forge/github"
	"github.com/orchard-suite/graft/internal/forge/gitlab"
	"github.com/orchard-suite/graft/internal/ghapp"
	"github.com/orchard-suite/graft/internal/hub"
	"github.com/orchard-suite/graft/internal/runtime/ion"
	"github.com/orchard-suite/graft/internal/sandbox"
	"github.com/orchard-suite/graft/internal/store"
	"github.com/orchard-suite/graft/internal/telemetry"
	"github.com/orchard-suite/graft/internal/web"
	"github.com/orchard-suite/graft/internal/workspace"
)

var version = "dev"

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(logger())
	case "workspace":
		err = serveWorkspace(logger())
	case "git-credential":
		err = gitCredential(os.Args[2:])
	case "tool":
		err = toolCommand(os.Args[2:], os.Stdout)
	case "evidence":
		err = evidenceCommand(os.Args[2:], os.Stdout)
	case "report":
		err = reportCommand(os.Args[2:], os.Stdout)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func logger() *slog.Logger {
	return kittelemetry.NewLogger(os.Stdout, "graft", version, kittelemetry.ParseLevel(os.Getenv("LOG_LEVEL")))
}

// newForge builds the program's forge adapter and its credential: a GitHub
// App that mints a short token per repository, or one token read from a
// file on every use, so a rotated token works at once.
func newForge(cfg config.Config) (forge.Forge, error) {
	prog := cfg.Program
	var tokens forge.Tokens
	if cfg.GitHubAppID != "" {
		key, err := os.ReadFile(cfg.GitHubAppKeyFile)
		if err != nil {
			return nil, err
		}
		var owners []string
		for _, r := range prog.Repositories {
			owner, _, _ := strings.Cut(r.Path, "/")
			owners = append(owners, owner)
		}
		app, err := ghapp.New(cfg.GitHubAppID, key, owners)
		if err != nil {
			return nil, err
		}
		app.API = github.APIBase(prog.Forge.URL)
		tokens = func(ctx context.Context, repo string) (string, error) {
			owner, name, ok := strings.Cut(repo, "/")
			if !ok {
				return "", fmt.Errorf("%q is not owner/name", repo)
			}
			return app.Token(ctx, owner, name)
		}
	} else {
		file := cfg.ForgeTokenFile
		tokens = func(_ context.Context, repo string) (string, error) {
			// The one token reaches only the program's own repositories.
			if prog.Repository(repo) == nil {
				return "", fmt.Errorf("%s is not a repository of %s", repo, prog.Name)
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				return "", fmt.Errorf("forge token: %w", err)
			}
			tok := strings.TrimSpace(string(raw))
			if tok == "" {
				return "", fmt.Errorf("forge token: %s is empty", file)
			}
			return tok, nil
		}
	}
	if prog.Forge.Kind == "gitlab" {
		return gitlab.New(prog.Forge.URL, tokens), nil
	}
	return github.New(prog.Forge.URL, tokens), nil
}

func serve(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	for _, dir := range []string{cfg.DataDir, cfg.WorkDir, cfg.RepoDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "graft.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	flush, err := kittelemetry.Setup(ctx, "graft", version)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = flush(ctx)
	}()

	if cfg.AuthMode == auth.ModeNone {
		log.Warn("authentication is off; every request is the local user", "user", auth.LocalUser)
	}
	prog := cfg.Program
	fg, err := newForge(cfg)
	if err != nil {
		return err
	}
	// Every git the runner starts, release-damnit's among them, asks this
	// binary for the forge's credential.
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	for _, kv := range forge.GitEnv(prog.Forge.URL, "!'"+exe+"' git-credential") {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	h, err := hub.New(prog, cfg.OrreryURL, cfg.OrreryToken, cfg.OrreryPushToken, cfg.OrreryPublicURL, log)
	if err != nil {
		return err
	}
	incidents := dispatch.New(cfg.DispatchURL, cfg.DispatchToken)
	if incidents == nil {
		log.Warn("no incident desk: no incident is checked or opened, and nobody is told", "reason", "GRAFT_DISPATCH_URL or GRAFT_DISPATCH_TOKEN unset")
	}
	// Where agent sessions run: beside Graft in this pod, or each in a
	// pod of its own.
	rt := ion.New()
	var boxes desk.Sandboxes = &desk.Shared{Workspace: workspace.NewClient(cfg.WorkspaceURL), Runtime: rt}
	if cfg.Sandbox == config.SandboxKubernetes {
		k, err := sandbox.InCluster(cfg.SandboxNamespace, cfg.SandboxPodFile, cfg.WorkDir, cfg.SandboxPort, cfg.SandboxStartWait, log)
		if err != nil {
			return err
		}
		boxes = k
	}
	d := desk.New(cfg, desk.Deps{Store: st, Forge: fg, Sandboxes: boxes, Hub: h, Incidents: incidents,
		Annals: annals.New(cfg.AnnalsURL, cfg.AnnalsToken, rt.Name(), log), Log: log})

	// What Graft declares to the suite: its lanes to the hub, its notice
	// kinds and its heartbeat to the incident desk. Each is tried again
	// until it lands; requests are worked meanwhile.
	go declare(ctx, log, "lane actions", h.DeclareLanes)
	go declare(ctx, log, "requests in the catalog", d.ShowAll)
	go every(ctx, log, "catalog heartbeat", 30*time.Minute, h.Alive)
	if incidents != nil {
		go declare(ctx, log, "notice kinds", func(ctx context.Context) error { return incidents.DeclareKinds(ctx, desk.Kinds) })
		// A Graft that stops is noticed at the incident desk.
		go every(ctx, log, "incident desk heartbeat", time.Minute, func(ctx context.Context) error { return incidents.Heartbeat(ctx, "graft", 5*time.Minute) })
	}
	go d.Serve(ctx)

	if cfg.OTLPEndpoint != "" {
		p := &telemetry.Pusher{Endpoint: cfg.OTLPEndpoint, Every: cfg.MetricsEvery, Store: st, Log: log, HTTP: &http.Client{Timeout: 15 * time.Second}}
		go p.Run(ctx)
	} else {
		log.Info("request metrics off", "reason", "GRAFT_OTLP_ENDPOINT unset")
	}

	site := web.New(st, d, prog, log, version, cfg.PublicURL)
	servers := []*http.Server{
		{Addr: cfg.Listen, Handler: kittelemetry.Handler(site.Session(auth.Middleware(cfg.AuthMode, log)), "session"), ReadHeaderTimeout: 10 * time.Second},
		// The agent's CLI reaches this one from inside the sandbox.
		{Addr: cfg.HandleListen, Handler: kittelemetry.Handler(site.Handle(), "handle"), ReadHeaderTimeout: 10 * time.Second},
	}
	if cfg.IntakeTokenDir != "" {
		servers = append(servers, &http.Server{Addr: cfg.APIListen, Handler: kittelemetry.Handler(site.Intake(auth.Apps(cfg.IntakeTokenDir, log)), "intake"), ReadHeaderTimeout: 10 * time.Second})
	} else {
		log.Info("token port off", "reason", "GRAFT_INTAKE_TOKEN_DIR unset")
	}
	// The forge says a ticket was commented on here; without it, a sweep
	// reads each waiting plan's ticket every few minutes.
	if cfg.HookSecretFile != "" {
		servers = append(servers, &http.Server{Addr: cfg.HookListen, Handler: kittelemetry.Handler(site.Hook(cfg.HookSecretFile), "hook"), ReadHeaderTimeout: 10 * time.Second})
	} else {
		log.Info("forge port off: tickets are read for comments by the sweep alone", "reason", "GRAFT_HOOK_SECRET_FILE unset")
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, srv := range servers {
			_ = srv.Shutdown(shutdown)
		}
	}()
	errs := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", srv.Addr, err)
				return
			}
			errs <- nil
		}()
	}
	log.Info("listening", "session", cfg.Listen, "intake", cfg.IntakeTokenDir != "", "intakeAddr", cfg.APIListen, "handle", cfg.HandleListen, "hook", cfg.HookSecretFile != "", "hookAddr", cfg.HookListen, "version", version,
		"auth", cfg.AuthMode, "program", prog.Name, "forge", prog.Forge.Kind, "repositories", len(prog.Repositories), "lanes", len(prog.Lanes), "trusted", prog.Trusted, "sandbox", cfg.Sandbox,
		"notices", prog.Notices)
	if prog.Holding() {
		log.Warn("notices held: nothing is sent to the incident desk; each notice is kept on its request as an event", "notices", prog.Notices)
	}
	// The first server to stop ends the process; a port that fails to bind
	// is an error, not a half-served Graft.
	err = <-errs
	stop()
	return err
}

// declare runs one declaration until it lands, a minute apart.
func declare(ctx context.Context, log *slog.Logger, what string, do func(context.Context) error) {
	for {
		err := do(ctx)
		if err == nil {
			log.Info("declared", "what", what)
			return
		}
		log.Warn("not declared; trying again in a minute", "what", what, "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// every runs one call now and then once each period until the context
// ends. A call that fails is logged and tried at the next period.
func every(ctx context.Context, log *slog.Logger, what string, period time.Duration, do func(context.Context) error) {
	for {
		if err := do(ctx); err != nil && ctx.Err() == nil {
			log.Warn("not sent; trying again at the next period", "what", what, "period", period.String(), "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(period):
		}
	}
}

// gitCredential is git's credential helper: `get` answers with the forge's
// credential for the repository git asks about. Git stores and erases
// nothing here.
func gitCredential(args []string) error {
	if len(args) == 0 || args[0] != "get" {
		return nil
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	fg, err := newForge(cfg)
	if err != nil {
		return err
	}
	return fg.Credential(context.Background(), os.Stdin, os.Stdout)
}

// serveWorkspace serves the work directory to the runner. It runs in a
// container of its own that holds no credential: beside the runner, or in a
// sandbox pod, where it takes the token the runner gave that pod.
func serveWorkspace(log *slog.Logger) error {
	cfg := config.WorkspaceFromEnv()
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: cfg.Listen, Handler: (&workspace.Server{Root: cfg.WorkDir, Token: cfg.Token}).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("workspace listening", "addr", cfg.Listen, "root", cfg.WorkDir, "token", cfg.Token != "", "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
