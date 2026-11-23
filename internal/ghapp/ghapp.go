// Package ghapp signs in to GitHub as Graft's GitHub App. Every token it
// hands out lasts an hour, reaches one owner's installation, and is
// narrowed to one repository when one is named. Owners outside the list Graft was given get nothing, so a
// stranger who installs the App never gets a token used on their account.
package ghapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// App mints installation tokens for the owners it may act for.
type App struct {
	ID     string
	key    *rsa.PrivateKey
	owners map[string]bool

	// API is GitHub's REST API; tests point it elsewhere.
	API  string
	HTTP *http.Client
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]token
}

type token struct {
	value   string
	expires time.Time
}

// New reads the App's private key (PEM, PKCS#1 or PKCS#8) and allows tokens
// for owners only.
func New(id string, keyPEM []byte, owners []string) (*App, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("ghapp: the private key is not PEM")
	}
	var key *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = k
	} else if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("ghapp: the private key is not RSA")
		}
		key = rk
	} else {
		return nil, fmt.Errorf("ghapp: private key: %w", err)
	}
	a := &App{ID: id, key: key, owners: map[string]bool{}, API: "https://api.github.com", HTTP: &http.Client{Timeout: 30 * time.Second}, now: time.Now, cache: map[string]token{}}
	for _, o := range owners {
		a.owners[strings.ToLower(o)] = true
	}
	return a, nil
}

// Token returns an installation token for owner, narrowed to repo unless
// repo is empty. A token is reused until five minutes before it expires.
func (a *App) Token(ctx context.Context, owner, repo string) (string, error) {
	if !a.owners[strings.ToLower(owner)] {
		return "", fmt.Errorf("ghapp: %s is not an owner Graft acts for", owner)
	}
	k := strings.ToLower(owner + "/" + repo)
	a.mu.Lock()
	t, ok := a.cache[k]
	a.mu.Unlock()
	if ok && a.now().Before(t.expires.Add(-5*time.Minute)) {
		return t.value, nil
	}
	jwt, err := a.jwt()
	if err != nil {
		return "", err
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := a.call(ctx, http.MethodGet, "/users/"+owner+"/installation", jwt, nil, &inst); err != nil {
		return "", fmt.Errorf("ghapp: the App is not installed on %s: %w", owner, err)
	}
	var body any
	if repo != "" {
		body = map[string][]string{"repositories": {repo}}
	}
	var res struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := a.call(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), jwt, body, &res); err != nil {
		return "", fmt.Errorf("ghapp: token for %s: %w", k, err)
	}
	a.mu.Lock()
	a.cache[k] = token{value: res.Token, expires: res.ExpiresAt}
	a.mu.Unlock()
	return res.Token, nil
}

// jwt is the App's own short-lived sign-in, which only asks for tokens.
func (a *App) jwt() (string, error) {
	enc := base64.RawURLEncoding
	now := a.now()
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": a.ID})
	if err != nil {
		return "", err
	}
	signed := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

func (a *App) call(ctx context.Context, method, path, jwt string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.API+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("%s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}
