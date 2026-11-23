// Package sandbox opens a pod of its own for each agent session, in a
// Kubernetes cluster. The pod runs the agent runtime's engine and a
// workspace; Graft reaches the workspace over the cluster network with a
// token made for that pod alone, and drives the agent through it. The pod
// is deleted when the session ends, so no session can read what another
// one left, and nothing an agent wrote runs beside Graft's credentials.
//
// The estate owns the pod's shape: Graft reads a Pod manifest from a file
// and changes only its name, its labels, and the workspace's token.
package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/orchard-suite/graft/internal/desk"
	"github.com/orchard-suite/graft/internal/runtime/ion"
	"github.com/orchard-suite/graft/internal/workspace"
)

// WorkspaceContainer is the container of the pod manifest that runs
// `graft workspace`. It is given the pod's token.
const WorkspaceContainer = "workspace"

// The settings a sandbox's workspace reads from its environment.
const (
	TokenEnv  = "GRAFT_WORKSPACE_TOKEN"
	ListenEnv = "GRAFT_WORKSPACE_LISTEN"
)

const inCluster = "/var/run/secrets/kubernetes.io/serviceaccount"

// Kubernetes opens one pod per agent session.
type Kubernetes struct {
	// API is the cluster's API address and Token reads the credential for
	// it; HTTP trusts the cluster's certificate.
	API   string
	Token func() (string, error)
	HTTP  *http.Client
	// Namespace is where the pods are made.
	Namespace string
	// Pod is the manifest every sandbox starts from.
	Pod map[string]any
	// Port is the workspace's port in the pod.
	Port int
	// StartWait is how long a pod may take to be ready.
	StartWait time.Duration
	// WorkDir is the work directory inside the pod.
	WorkDir string
	Log     *slog.Logger
	// address is how the workspace of a ready pod is reached; tests set it.
	address func(podIP string, port int) string
}

// InCluster returns a provider that makes pods in namespace from the
// manifest at podFile, as the pod Graft itself runs in.
func InCluster(namespace, podFile, workDir string, port int, startWait time.Duration, log *slog.Logger) (*Kubernetes, error) {
	raw, err := os.ReadFile(podFile)
	if err != nil {
		return nil, fmt.Errorf("sandbox pod file: %w", err)
	}
	pod, err := ParsePod(raw)
	if err != nil {
		return nil, fmt.Errorf("sandbox pod file %s: %w", podFile, err)
	}
	ca, err := os.ReadFile(inCluster + "/ca.crt")
	if err != nil {
		return nil, fmt.Errorf("the cluster's certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("the cluster's certificate is not PEM")
	}
	if namespace == "" {
		ns, err := os.ReadFile(inCluster + "/namespace")
		if err != nil {
			return nil, fmt.Errorf("the pod's namespace: %w", err)
		}
		namespace = strings.TrimSpace(string(ns))
	}
	host, port443 := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		return nil, errors.New("not in a cluster: KUBERNETES_SERVICE_HOST is unset")
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &Kubernetes{
		API: "https://" + host + ":" + port443,
		// The token rotates; read it on every call.
		Token: func() (string, error) {
			b, err := os.ReadFile(inCluster + "/token")
			return strings.TrimSpace(string(b)), err
		},
		HTTP:      &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
		Namespace: namespace, Pod: pod, Port: port, StartWait: startWait, WorkDir: workDir, Log: log,
	}, nil
}

// ParsePod reads a Pod manifest and checks it has what a sandbox needs: a
// container named workspace.
func ParsePod(raw []byte) (map[string]any, error) {
	var pod map[string]any
	if err := yaml.Unmarshal(raw, &pod); err != nil {
		return nil, err
	}
	if pod["kind"] != "Pod" {
		return nil, fmt.Errorf("kind is %v, want Pod", pod["kind"])
	}
	if _, err := container(pod, WorkspaceContainer); err != nil {
		return nil, err
	}
	return pod, nil
}

func container(pod map[string]any, name string) (map[string]any, error) {
	spec, _ := pod["spec"].(map[string]any)
	for _, list := range []string{"containers", "initContainers"} {
		items, _ := spec[list].([]any)
		for _, item := range items {
			if c, ok := item.(map[string]any); ok && c["name"] == name {
				return c, nil
			}
		}
	}
	return nil, fmt.Errorf("the pod has no container named %s", name)
}

// podName is the pod for one session of one request.
func podName(f desk.BoxFor) string { return "graft-" + f.Request + "-" + f.Stage }

// manifest is the pod for one session: the estate's manifest with its name,
// its labels, and the workspace's token and port.
func (k *Kubernetes) manifest(f desk.BoxFor, token string) (map[string]any, error) {
	raw, err := json.Marshal(k.Pod)
	if err != nil {
		return nil, err
	}
	var pod map[string]any
	if err := json.Unmarshal(raw, &pod); err != nil {
		return nil, err
	}
	meta, _ := pod["metadata"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	delete(meta, "generateName")
	meta["name"], meta["namespace"] = podName(f), k.Namespace
	labels, _ := meta["labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels["graft.request"], labels["graft.stage"] = f.Request, f.Stage
	meta["labels"] = labels
	pod["metadata"] = meta
	spec, _ := pod["spec"].(map[string]any)
	spec["restartPolicy"] = "Never"
	ws, err := container(pod, WorkspaceContainer)
	if err != nil {
		return nil, err
	}
	env, _ := ws["env"].([]any)
	kept := env[:0:0]
	for _, e := range env {
		if m, ok := e.(map[string]any); ok && (m["name"] == TokenEnv || m["name"] == ListenEnv) {
			continue
		}
		kept = append(kept, e)
	}
	ws["env"] = append(kept,
		map[string]any{"name": TokenEnv, "value": token},
		map[string]any{"name": ListenEnv, "value": fmt.Sprintf(":%d", k.Port)})
	return pod, nil
}

// call sends one request to the cluster's API.
func (k *Kubernetes) call(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.API+path, body)
	if err != nil {
		return 0, err
	}
	tok, err := k.Token()
	if err != nil {
		return 0, fmt.Errorf("the cluster token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := k.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return res.StatusCode, fmt.Errorf("%s %s: %d: %s", method, path, res.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, err
		}
	}
	return res.StatusCode, nil
}

func (k *Kubernetes) pods() string { return "/api/v1/namespaces/" + k.Namespace + "/pods" }

// status is the part of a pod's state a sandbox waits on.
type status struct {
	Status struct {
		Phase      string `json:"phase"`
		PodIP      string `json:"podIP"`
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name  string `json:"name"`
			State struct {
				Terminated *struct {
					ExitCode int    `json:"exitCode"`
					Reason   string `json:"reason"`
				} `json:"terminated"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

// stopped names a container that has ended. A sandbox pod never restarts
// one, so the pod can no longer become ready.
func (s status) stopped() string {
	for _, c := range s.Status.ContainerStatuses {
		if t := c.State.Terminated; t != nil {
			return fmt.Sprintf("container %s ended with code %d (%s)", c.Name, t.ExitCode, t.Reason)
		}
	}
	return ""
}

func (s status) ready() bool {
	if s.Status.Phase != "Running" || s.Status.PodIP == "" {
		return false
	}
	for _, c := range s.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

// remove deletes the session's pod and waits until it is gone.
func (k *Kubernetes) remove(ctx context.Context, name string) error {
	code, err := k.call(ctx, http.MethodDelete, k.pods()+"/"+name+"?gracePeriodSeconds=0", nil, nil)
	if code == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		if code, _ := k.call(ctx, http.MethodGet, k.pods()+"/"+name, nil, nil); code == http.StatusNotFound {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Open makes the session's pod, waits until it is ready, and returns its
// box. A pod left from a run that was cut short is replaced.
func (k *Kubernetes) Open(ctx context.Context, f desk.BoxFor) (*desk.Box, error) {
	name := podName(f)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(b)
	pod, err := k.manifest(f, token)
	if err != nil {
		return nil, err
	}
	if err := k.remove(ctx, name); err != nil {
		return nil, fmt.Errorf("sandbox %s: %w", name, err)
	}
	if _, err := k.call(ctx, http.MethodPost, k.pods(), pod, nil); err != nil {
		return nil, fmt.Errorf("sandbox %s: %w", name, err)
	}
	shut := func(ctx context.Context) {
		if err := k.remove(ctx, name); err != nil {
			k.Log.Error("sandbox not removed", "pod", name, "error", err)
			return
		}
		k.Log.Info("sandbox shut", "pod", name)
	}
	wait, cancel := context.WithTimeout(ctx, k.StartWait)
	defer cancel()
	var st status
	for {
		code, err := k.call(wait, http.MethodGet, k.pods()+"/"+name, nil, &st)
		if err != nil && code != http.StatusNotFound && wait.Err() == nil {
			k.Log.Warn("sandbox not read; reading again", "pod", name, "error", err)
		}
		if st.ready() {
			break
		}
		if st.Status.Phase == "Failed" || st.Status.Phase == "Succeeded" {
			shut(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("sandbox %s ended %s before it was ready", name, st.Status.Phase)
		}
		if why := st.stopped(); why != "" {
			shut(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("sandbox %s cannot start: %s", name, why)
		}
		select {
		case <-wait.Done():
			shut(context.WithoutCancel(ctx))
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("sandbox %s was not ready when its stage ran out of time (phase %s): %w", name, st.Status.Phase, err)
			}
			return nil, fmt.Errorf("sandbox %s was not ready within %s (phase %s)", name, k.StartWait, st.Status.Phase)
		case <-time.After(2 * time.Second):
		}
	}
	address := k.address
	if address == nil {
		address = func(ip string, port int) string {
			if strings.Contains(ip, ":") {
				ip = "[" + ip + "]"
			}
			return fmt.Sprintf("http://%s:%d", ip, port)
		}
	}
	ws := workspace.NewClient(address(st.Status.PodIP, k.Port))
	ws.Token = token
	k.Log.Info("sandbox open", "pod", name, "stage", f.Stage)
	return &desk.Box{WS: ws, RT: &ion.Remote{In: ws, Scratch: k.WorkDir}, Close: shut}, nil
}
