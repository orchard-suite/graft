package ghapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGitHub answers the two calls a token takes, checks the App's JWT, and
// records what each token was narrowed to.
func fakeGitHub(t *testing.T, key *rsa.PrivateKey) (*httptest.Server, *[]string) {
	t.Helper()
	var asked []string
	mux := http.NewServeMux()
	verify := func(r *http.Request) bool {
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			return false
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return false
		}
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		return rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) == nil && strings.Contains(string(claims), `"iss":"42"`)
	}
	mux.HandleFunc("GET /users/{owner}/installation", func(w http.ResponseWriter, r *http.Request) {
		if !verify(r) {
			http.Error(w, "bad jwt", http.StatusUnauthorized)
			return
		}
		if r.PathValue("owner") != "acme" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"id":7}`))
	})
	mux.HandleFunc("POST /app/installations/7/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if !verify(r) {
			http.Error(w, "bad jwt", http.StatusUnauthorized)
			return
		}
		var body struct {
			Repositories []string `json:"repositories"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked = append(asked, strings.Join(body.Repositories, ","))
		json.NewEncoder(w).Encode(map[string]any{"token": "tok-" + strings.Join(body.Repositories, ","), "expires_at": time.Now().Add(time.Hour)})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &asked
}

func newApp(t *testing.T) (*App, *[]string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv, asked := fakeGitHub(t, key)
	a, err := New("42", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), []string{"ACME", "other"})
	if err != nil {
		t.Fatal(err)
	}
	a.API = srv.URL
	return a, asked
}

func TestTokenIsNarrowedAndReused(t *testing.T) {
	a, asked := newApp(t)
	ctx := context.Background()
	for range 2 {
		tok, err := a.Token(ctx, "acme", "app")
		if err != nil || tok != "tok-app" {
			t.Fatalf("token %q, %v", tok, err)
		}
	}
	if tok, err := a.Token(ctx, "acme", ""); err != nil || tok != "tok-" {
		t.Fatalf("owner-wide token %q, %v", tok, err)
	}
	if strings.Join(*asked, "|") != "app|" {
		t.Fatalf("GitHub was asked for %q; a live token is reused", *asked)
	}
}

func TestTokenOnlyForListedOwners(t *testing.T) {
	a, asked := newApp(t)
	if _, err := a.Token(context.Background(), "stranger", "repo"); err == nil || len(*asked) != 0 {
		t.Fatalf("a token for an unlisted owner: %v, asked %v", err, *asked)
	}
	if _, err := a.Token(context.Background(), "other", "repo"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("an owner without the App installed: %v", err)
	}
}
