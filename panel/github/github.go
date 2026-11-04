// Package github talks to the GitHub API on behalf of a GitHub App: turning
// a manifest into an app, and listing and getting tokens for its
// installations.
package github

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
	"strconv"
	"time"
)

type Client struct {
	API  string // https://api.github.com
	HTTP *http.Client
}

func New() *Client {
	return &Client{API: "https://api.github.com", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// App is what GitHub returns after creating an app from a manifest.
type App struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
}

type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
	HTMLURL string `json:"html_url"`
}

type Repository struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Message) }

func (c *Client) do(ctx context.Context, method, path, auth string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(data, &e)
		return &Error{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ConvertManifest finishes the manifest flow: GitHub gives the code to the
// redirect URL, and this exchanges it for the new app and its credentials.
func (c *Client) ConvertManifest(ctx context.Context, code string) (App, error) {
	var app App
	err := c.do(ctx, http.MethodPost, "/app-manifests/"+code+"/conversions", "", nil, &app)
	if err == nil && (app.ID == 0 || app.PEM == "" || app.WebhookSecret == "") {
		err = errors.New("GitHub's answer is missing the app's id, key or webhook secret")
	}
	return app, err
}

// ParseKey reads an app's PEM private key.
func ParseKey(pemText []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemText)
	if block == nil {
		return nil, errors.New("no PEM block in the app's private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the app's private key is not an RSA key")
	}
	return rsaKey, nil
}

// AppJWT makes the short-lived token that authenticates as the app itself.
func AppJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"RS256","typ":"JWT"}`))
	// Backdated a minute for clock drift; GitHub allows at most ten minutes.
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(appID, 10)})
	signing := header + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc(sig), nil
}

func (c *Client) appAuth(appID int64, key *rsa.PrivateKey) (string, error) {
	jwt, err := AppJWT(appID, key, time.Now())
	if err != nil {
		return "", err
	}
	return "Bearer " + jwt, nil
}

func (c *Client) Installations(ctx context.Context, appID int64, key *rsa.PrivateKey) ([]Installation, error) {
	auth, err := c.appAuth(appID, key)
	if err != nil {
		return nil, err
	}
	var out []Installation
	err = c.do(ctx, http.MethodGet, "/app/installations?per_page=100", auth, nil, &out)
	return out, err
}

// InstallationToken gets a token for an installation. With repos, it only
// reads the contents of those repositories, and it expires within an hour.
func (c *Client) InstallationToken(ctx context.Context, appID int64, key *rsa.PrivateKey, installationID int64, repos []string) (string, error) {
	auth, err := c.appAuth(appID, key)
	if err != nil {
		return "", err
	}
	body := map[string]any{"permissions": map[string]string{"contents": "read"}}
	if len(repos) > 0 {
		body["repositories"] = repos
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(installationID, 10)+"/access_tokens", auth, body, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("GitHub returned no token")
	}
	return out.Token, nil
}

// InstallationRepos lists the repositories an installation can read.
func (c *Client) InstallationRepos(ctx context.Context, token string) ([]Repository, error) {
	var all []Repository
	for page := 1; page <= 10; page++ {
		var out struct {
			Repositories []Repository `json:"repositories"`
		}
		if err := c.do(ctx, http.MethodGet, "/installation/repositories?per_page=100&page="+strconv.Itoa(page), "token "+token, nil, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Repositories...)
		if len(out.Repositories) < 100 {
			break
		}
	}
	return all, nil
}
