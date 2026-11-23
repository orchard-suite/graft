package web

import (
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// openapi.yaml describes exactly the routes of the page's port, the token
// port, and the forge's port, so the definition the catalog links to never
// drifts from the code.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml: %v", err)
	}
	methods := []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions, http.MethodTrace}
	var spec []string
	for path, item := range doc.Paths {
		for key := range item {
			if m := strings.ToUpper(key); slices.Contains(methods, m) {
				spec = append(spec, m+" "+path)
			}
		}
	}
	var code []string
	s := &Server{}
	for _, rt := range append(append(s.sessionRoutes(), s.intakeRoutes()...), s.hookRoutes()...) {
		code = append(code, rt.pattern)
	}
	slices.Sort(spec)
	slices.Sort(code)
	for _, p := range code {
		if !slices.Contains(spec, p) {
			t.Errorf("server.go serves %q, which openapi.yaml does not describe: update openapi.yaml", p)
		}
	}
	for _, p := range spec {
		if !slices.Contains(code, p) {
			t.Errorf("openapi.yaml describes %q, which server.go does not serve: update openapi.yaml", p)
		}
	}
}
