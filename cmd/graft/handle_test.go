package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/orchard-suite/graft/internal/desk"
)

func TestFindHandleWalksUpFromTheClone(t *testing.T) {
	home := t.TempDir()
	deep := filepath.Join(home, "repo", "src", "pkg")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := findHandle(deep); err == nil {
		t.Fatal("found a handle where there is none")
	}
	if err := os.WriteFile(filepath.Join(home, desk.HandleFile), []byte(`{"url":"http://127.0.0.1:8082","secret":"s3cret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := findHandle(deep)
	if err != nil || h.Secret != "s3cret" || h.URL != "http://127.0.0.1:8082" {
		t.Fatalf("handle: %+v %v", h, err)
	}
}

func TestParseCall(t *testing.T) {
	id, env, inputs, err := parseCall([]string{"queue-depth", "--env", "production", "queue=orders", "filter=a=b"})
	if err != nil || id != "queue-depth" || env != "production" || !reflect.DeepEqual(inputs, map[string]any{"queue": "orders", "filter": "a=b"}) {
		t.Fatalf("%q %q %v %v", id, env, inputs, err)
	}
	if id, env, _, err := parseCall([]string{"--env=nonproduction", "restart"}); err != nil || id != "restart" || env != "nonproduction" {
		t.Fatalf("%q %q %v", id, env, err)
	}
	for _, bad := range [][]string{{}, {"--env"}, {"queue=orders"}} {
		if _, _, _, err := parseCall(bad); err == nil {
			t.Errorf("%v parsed", bad)
		}
	}
}
