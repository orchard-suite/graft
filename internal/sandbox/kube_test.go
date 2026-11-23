package sandbox

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orchard-suite/graft/internal/desk"
	"github.com/orchard-suite/graft/internal/runtime"
	"github.com/orchard-suite/graft/internal/shell"
	"github.com/orchard-suite/graft/internal/workspace"
)

const podFile = `
apiVersion: v1
kind: Pod
metadata:
  generateName: ignored-
  labels: { app: graft-sandbox }
spec:
  automountServiceAccountToken: false
  containers:
  - name: engine
    image: example.com/graft:1
    command: [ion, serve]
  - name: workspace
    image: example.com/graft:1
    command: [graft, workspace]
    env:
    - { name: GRAFT_WORK_DIR, value: /work }
    - { name: GRAFT_WORKSPACE_TOKEN, value: a-token-the-estate-must-not-set }
`

// cluster is a fake API server that keeps pods, and a real workspace that
// stands in for the pod's own, taking whatever token the pod was given.
type cluster struct {
	mu      sync.Mutex
	pods    map[string]map[string]any
	deleted []string
	created []map[string]any
	token   string
	work    string
	// status is what a read of a pod answers, when set.
	status string
}

func newCluster(t *testing.T) (*Kubernetes, *cluster) {
	t.Helper()
	c := &cluster{pods: map[string]map[string]any{}, work: t.TempDir()}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer cluster-token" {
			t.Errorf("%s %s came without the cluster token", r.Method, r.URL.Path)
		}
		const base = "/api/v1/namespaces/sandboxes/pods"
		name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, base), "/")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == base:
			var pod map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &pod)
			c.created = append(c.created, pod)
			c.pods[pod["metadata"].(map[string]any)["name"].(string)] = pod
			ws, _ := container(pod, WorkspaceContainer)
			for _, e := range ws["env"].([]any) {
				if m := e.(map[string]any); m["name"] == TokenEnv {
					c.token = m["value"].(string)
				}
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(raw)
		case r.Method == http.MethodGet && c.pods[name] != nil && c.status != "":
			_, _ = w.Write([]byte(c.status))
		case r.Method == http.MethodGet && c.pods[name] != nil:
			_, _ = w.Write([]byte(`{"status":{"phase":"Running","podIP":"10.0.0.7","conditions":[{"type":"Ready","status":"True"}]}}`))
		case r.Method == http.MethodDelete && c.pods[name] != nil:
			delete(c.pods, name)
			c.deleted = append(c.deleted, name)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	pod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		token := c.token
		c.mu.Unlock()
		(&workspace.Server{Root: c.work, Token: token}).Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(pod.Close)
	manifest, err := ParsePod([]byte(podFile))
	if err != nil {
		t.Fatal(err)
	}
	k := &Kubernetes{API: api.URL, Token: func() (string, error) { return "cluster-token", nil }, HTTP: api.Client(), Namespace: "sandboxes", Pod: manifest,
		Port: 8090, StartWait: 10 * time.Second, WorkDir: c.work, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		address: func(string, int) string { return pod.URL }}
	return k, c
}

func TestEachSessionGetsItsOwnPodAndLosesItAtTheEnd(t *testing.T) {
	k, c := newCluster(t)
	ctx := context.Background()
	box, err := k.Open(ctx, desk.BoxFor{Request: "abc", Stage: "investigate", Key: "graft-abc-investigate", Home: filepath.Join(c.work, "requests", "abc")})
	if err != nil {
		t.Fatal(err)
	}
	pod := c.created[0]
	meta := pod["metadata"].(map[string]any)
	if meta["name"] != "graft-abc-investigate" || meta["namespace"] != "sandboxes" || meta["generateName"] != nil {
		t.Errorf("metadata: %v", meta)
	}
	labels := meta["labels"].(map[string]any)
	if labels["graft.request"] != "abc" || labels["graft.stage"] != "investigate" || labels["app"] != "graft-sandbox" {
		t.Errorf("labels: %v", labels)
	}
	if pod["spec"].(map[string]any)["restartPolicy"] != "Never" {
		t.Error("a sandbox pod must never restart")
	}
	if c.token == "" || c.token == "a-token-the-estate-must-not-set" || len(c.token) < 32 {
		t.Errorf("the workspace's token is not Graft's own: %q", c.token)
	}
	// The box reaches the pod's workspace with that token.
	res, err := box.WS.Run(ctx, io.Discard, shell.Cmd{Dir: c.work, Args: []string{"sh", "-c", "echo in the sandbox"}})
	if err != nil || strings.TrimSpace(res.Output) != "in the sandbox" {
		t.Fatalf("run: %q %v", res.Output, err)
	}
	stranger := workspace.NewClient(k.address("", 0))
	if _, err := stranger.Run(ctx, io.Discard, shell.Cmd{Dir: c.work, Args: []string{"true"}}); err == nil {
		t.Error("the sandbox ran a command for a caller without its token")
	}
	box.Close(ctx)
	if len(c.deleted) != 1 || c.deleted[0] != "graft-abc-investigate" || len(c.pods) != 0 {
		t.Errorf("after the session: deleted %v, left %v", c.deleted, c.pods)
	}
}

func TestAPodWhoseContainerEndedFailsAtOnce(t *testing.T) {
	k, c := newCluster(t)
	c.status = `{"status":{"phase":"Running","podIP":"10.0.0.7","containerStatuses":[{"name":"workspace","state":{"running":{}}},{"name":"engine","state":{"terminated":{"exitCode":1,"reason":"Error"}}}]}}`
	start := time.Now()
	_, err := k.Open(context.Background(), desk.BoxFor{Request: "abc", Stage: "screen", Key: "graft-abc-screen", Home: c.work})
	if err == nil || !strings.Contains(err.Error(), "container engine ended with code 1") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second || len(c.deleted) != 1 {
		t.Errorf("took %s, deleted %v", time.Since(start), c.deleted)
	}
}

func TestAPodLeftBehindIsReplaced(t *testing.T) {
	k, c := newCluster(t)
	ctx := context.Background()
	f := desk.BoxFor{Request: "abc", Stage: "build", Key: "graft-abc-build", Home: c.work}
	if _, err := k.Open(ctx, f); err != nil {
		t.Fatal(err)
	}
	first := c.token
	if _, err := k.Open(ctx, f); err != nil {
		t.Fatal(err)
	}
	if len(c.created) != 2 || len(c.deleted) != 1 || c.token == first {
		t.Errorf("created %d, deleted %v, same token %v", len(c.created), c.deleted, c.token == first)
	}
}

func TestTheAgentRunsInsideThePod(t *testing.T) {
	k, c := newCluster(t)
	ctx := context.Background()
	// A stand-in for the ion CLI: it writes its arguments and its stdin.
	bin := filepath.Join(t.TempDir(), "ion")
	// It ends as the ion CLI does: with the run's report, on stderr.
	script := "#!/bin/sh\necho \"args: $*\"\necho \"prompt: $(cat)\"\n[ \"$1\" = prompt ] && [ -n \"$FAIL\" ] && exit 124\n" +
		"echo 'Run: {\"model\":\"m\",\"costUsd\":0.42,\"turns\":3,\"durationMs\":4200,\"inputTokens\":100,\"outputTokens\":20,\"cacheReadTokens\":5000,\"cacheCreationTokens\":300}' >&2\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	box, err := k.Open(ctx, desk.BoxFor{Request: "abc", Stage: "build", Key: "graft-abc-build", Home: c.work})
	if err != nil {
		t.Fatal(err)
	}
	out, use, err := box.RT.Ask(ctx, runtime.Session{Dir: c.work, Key: "graft-abc-build", Model: "m", Timeout: time.Minute, Budget: 2, Prompt: "build the plan\nwith two lines"})
	if err != nil {
		t.Fatal(err)
	}
	if use.From != runtime.FromRun || use.CostUSD != 0.42 || use.Turns != 3 || use.CacheReadTokens != 5000 || use.Duration != 4200*time.Millisecond {
		t.Errorf("the run's report did not cross the workspace: %+v", use)
	}
	if strings.Contains(out, "Run: ") {
		t.Errorf("the answer carries the runtime's report: %q", out)
	}
	for _, want := range []string{"args: prompt --attach --model m", "--key graft-abc-build", "--max-budget 2.00", "prompt: build the plan\nwith two lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("the agent's output %q lacks %q", out, want)
		}
	}
	left, _ := filepath.Glob(filepath.Join(c.work, ".prompt-*"))
	if len(left) != 0 {
		t.Errorf("prompt files left behind: %v", left)
	}
}

func TestParsePodNeedsAWorkspace(t *testing.T) {
	if _, err := ParsePod([]byte("kind: Pod\nspec: {containers: [{name: engine}]}")); err == nil {
		t.Error("a pod with no workspace container parsed")
	}
	if _, err := ParsePod([]byte("kind: Deployment\nspec: {}")); err == nil {
		t.Error("a Deployment parsed as a Pod")
	}
}
