package api

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arman-arif/control-portal/panel/github"
	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

var (
	githubOrgPattern  = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
	githubCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
)

const manifestStateTTL = 30 * time.Minute

// manifestState ties GitHub's redirect back to the account that started it,
// without storing anything: the user and expiry are sealed with the panel key.
func (a *API) manifestState(userID int64) string {
	b := make([]byte, 16)
	binary.BigEndian.PutUint64(b, uint64(userID))
	binary.BigEndian.PutUint64(b[8:], uint64(time.Now().Add(manifestStateTTL).Unix()))
	return base64.RawURLEncoding.EncodeToString(a.cfg.Secrets.Seal(b))
}

func (a *API) checkManifestState(state string, userID int64) bool {
	sealed, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return false
	}
	b, err := a.cfg.Secrets.Open(sealed)
	if err != nil || len(b) != 16 {
		return false
	}
	return int64(binary.BigEndian.Uint64(b)) == userID && time.Now().Unix() < int64(binary.BigEndian.Uint64(b[8:]))
}

// handleGitHubAppManifest returns what the web app posts to GitHub to create
// the app: GitHub's manifest flow needs a form submitted from the browser.
func (a *API) handleGitHubAppManifest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Organization string `json:"organization"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	org := strings.TrimSpace(in.Organization)
	if org != "" && !githubOrgPattern.MatchString(org) {
		writeError(w, http.StatusBadRequest, "that is not a GitHub organization name")
		return
	}
	host := strings.TrimPrefix(strings.TrimPrefix(a.cfg.PublicURL, "https://"), "http://")
	name := "Keel " + host
	if len(name) > 34 { // GitHub's limit
		name = name[:34]
	}
	manifest, _ := json.Marshal(map[string]any{
		"name":                name,
		"url":                 a.cfg.PublicURL,
		"hook_attributes":     map[string]any{"url": a.cfg.PublicURL + "/api/hooks/github-app", "active": true},
		"redirect_url":        a.cfg.PublicURL + "/api/github-app/callback",
		"setup_url":           a.cfg.PublicURL + "/settings",
		"setup_on_update":     true,
		"public":              false,
		"default_permissions": map[string]string{"contents": "read", "metadata": "read"},
		"default_events":      []string{"push"},
	})
	action := a.cfg.GitHubWeb + "/settings/apps/new"
	if org != "" {
		action = a.cfg.GitHubWeb + "/organizations/" + org + "/settings/apps/new"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"action":   action + "?state=" + url.QueryEscape(a.manifestState(userFrom(r).ID)),
		"manifest": string(manifest),
	})
}

// handleGitHubAppCallback is where GitHub sends the browser after creating
// the app. It swaps the code for the app's credentials.
func (a *API) handleGitHubAppCallback(w http.ResponseWriter, r *http.Request) {
	back := func(param, value string) {
		http.Redirect(w, r, "/settings?"+param+"="+url.QueryEscape(value), http.StatusSeeOther)
	}
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	userID := userFrom(r).ID
	if !githubCodePattern.MatchString(code) || !a.checkManifestState(state, userID) {
		back("github_error", "The link from GitHub was not valid or has expired. Please start again.")
		return
	}
	app, err := a.github.ConvertManifest(r.Context(), code)
	if err != nil {
		log.Printf("creating GitHub App: %v", err)
		back("github_error", "GitHub did not hand over the new app: "+err.Error())
		return
	}
	if _, err := github.ParseKey([]byte(app.PEM)); err != nil {
		back("github_error", "GitHub's key for the app could not be read: "+err.Error())
		return
	}
	err = a.store.SaveGitHubApp(r.Context(), store.GitHubApp{
		UserID: userID, AppID: app.ID, Slug: app.Slug, Name: app.Name, HTMLURL: app.HTMLURL,
		PrivateKey: a.cfg.Secrets.Seal([]byte(app.PEM)), WebhookSecret: a.cfg.Secrets.Seal([]byte(app.WebhookSecret)),
	})
	if err != nil {
		internalError(w, err)
		return
	}
	back("github", "created")
}

// githubApp loads the account's app and its private key.
func (a *API) githubApp(ctx context.Context, userID int64) (store.GitHubApp, *rsa.PrivateKey, error) {
	app, err := a.store.GitHubAppByUser(ctx, userID)
	if err != nil {
		return app, nil, err
	}
	pemText, err := a.cfg.Secrets.Open(app.PrivateKey)
	if err != nil {
		return app, nil, err
	}
	key, err := github.ParseKey(pemText)
	return app, key, err
}

func (a *API) handleGetGitHubApp(w http.ResponseWriter, r *http.Request) {
	app, key, err := a.githubApp(r.Context(), userFrom(r).ID)
	if isNotFound(err) {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false})
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	out := map[string]any{
		"configured":  true,
		"app":         app,
		"install_url": a.cfg.GitHubWeb + "/apps/" + app.Slug + "/installations/new",
	}
	if inst, err := a.github.Installations(r.Context(), app.AppID, key); err != nil {
		out["error"] = err.Error()
	} else {
		out["installations"] = inst
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleDeleteGitHubApp(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteGitHubApp(r.Context(), userFrom(r).ID); err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type appRepo struct {
	InstallationID int64  `json:"installation_id"`
	FullName       string `json:"full_name"`
	Private        bool   `json:"private"`
	DefaultBranch  string `json:"default_branch"`
}

func (a *API) appRepos(ctx context.Context, userID int64) ([]appRepo, error) {
	app, key, err := a.githubApp(ctx, userID)
	if err != nil {
		return nil, err
	}
	installations, err := a.github.Installations(ctx, app.AppID, key)
	if err != nil {
		return nil, err
	}
	out := []appRepo{}
	for _, inst := range installations {
		token, err := a.github.InstallationToken(ctx, app.AppID, key, inst.ID, nil)
		if err != nil {
			return nil, err
		}
		repos, err := a.github.InstallationRepos(ctx, token)
		if err != nil {
			return nil, err
		}
		for _, repo := range repos {
			out = append(out, appRepo{InstallationID: inst.ID, FullName: repo.FullName, Private: repo.Private, DefaultBranch: repo.DefaultBranch})
		}
	}
	return out, nil
}

func (a *API) handleGitHubAppRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := a.appRepos(r.Context(), userFrom(r).ID)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "no GitHub App is set up")
		return
	} else if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

// repoName is the part of a GitHub https URL after the owner, without .git.
func repoName(repoURL string) string {
	path := strings.TrimSuffix(strings.TrimPrefix(repoURL, protocol.GitHubHTTPSPrefix), ".git")
	_, name, _ := strings.Cut(path, "/")
	return name
}

// prepareDeploy adds a fresh, read-only token for the repository when the
// site deploys through the GitHub App.
func (a *API) prepareDeploy(ctx context.Context, job store.Job) (json.RawMessage, error) {
	if job.SiteID == nil {
		return job.Params, nil
	}
	site, err := a.store.GetSiteByID(ctx, *job.SiteID)
	if err != nil {
		return nil, err
	}
	if site.GitHubInstallationID == nil {
		return job.Params, nil
	}
	var p protocol.Deploy
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, err
	}
	app, key, err := a.githubApp(ctx, site.UserID)
	if isNotFound(err) {
		return nil, errors.New("the site deploys through a GitHub App that is no longer set up in Keel")
	} else if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p.Token, err = a.github.InstallationToken(ctx, app.AppID, key, *site.GitHubInstallationID, []string{repoName(p.RepoURL)})
	if err != nil {
		return nil, fmt.Errorf("getting a GitHub token: %w", err)
	}
	return json.Marshal(p)
}

// handleGitHubAppWebhook deploys the sites that follow a pushed branch. The
// request names its app, and is trusted only if signed with that app's
// webhook secret.
func (a *API) handleGitHubAppWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	appID, err := strconv.ParseInt(r.Header.Get("X-GitHub-Hook-Installation-Target-ID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown app")
		return
	}
	app, err := a.store.GitHubAppByAppID(r.Context(), appID)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "unknown app")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	secret, err := a.cfg.Secrets.Open(app.WebhookSecret)
	if err != nil {
		internalError(w, err)
		return
	}
	if !validSignature(secret, body, r.Header.Get("X-Hub-Signature-256")) {
		writeError(w, http.StatusUnauthorized, "bad signature")
		return
	}
	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	case "push":
	default:
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "event " + event + " is not a push"})
		return
	}
	var push struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		HeadCommit *struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"head_commit"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if err := json.Unmarshal(body, &push); err != nil {
		writeError(w, http.StatusBadRequest, "bad push payload")
		return
	}
	branch, isBranch := strings.CutPrefix(push.Ref, "refs/heads/")
	repoURL, err := protocol.NormalizeRepoURL(protocol.GitHubHTTPSPrefix + push.Repository.FullName)
	if !isBranch || push.Deleted || err != nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "not a push to a branch"})
		return
	}
	sites, err := a.store.SitesForPush(r.Context(), app.UserID, push.Installation.ID, repoURL, branch)
	if err != nil {
		internalError(w, err)
		return
	}
	sha, message := push.After, ""
	if push.HeadCommit != nil {
		sha, message = push.HeadCommit.ID, push.HeadCommit.Message
	}
	message, _, _ = strings.Cut(message, "\n")
	started := 0
	for _, site := range sites {
		if _, status, msg := a.startDeploy(r.Context(), site, "push", truncate(sha, 64), truncate(message, 500)); status != 0 {
			log.Printf("deploying site %d on push: %s", site.ID, msg)
			continue
		}
		started++
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "ok", "deploying": started})
}
