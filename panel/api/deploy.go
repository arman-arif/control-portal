package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

const maxWebhookBody = 5 << 20

func (a *API) webhookURL(siteID int64) string {
	return a.cfg.PublicURL + "/api/hooks/github/" + strconv.FormatInt(siteID, 10)
}

func deployScript(s store.Site) string {
	if s.DeployScript != nil {
		return *s.DeployScript
	}
	return protocol.DefaultDeployScriptV1
}

func (a *API) deployParams(s store.Site) protocol.Deploy {
	return protocol.Deploy{Site: a.siteParams(s), RepoURL: s.RepoURL, Branch: s.Branch, Script: deployScript(s), TimeoutMinutes: s.DeployTimeout}
}

// handleSetRepository saves where the site deploys from. For an SSH
// repository, the server makes the site's deploy key if it has none.
func (a *API) handleSetRepository(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		RepoURL string `json:"repo_url"`
		Branch  string `json:"branch"`
		// InstallationID deploys through the account's GitHub App.
		InstallationID *int64 `json:"installation_id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	repo, err := protocol.NormalizeRepoURL(in.RepoURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.InstallationID != nil {
		// The app clones over https with a token, and must be able to read the repository.
		repo, err = a.checkAppRepo(r.Context(), userFrom(r).ID, *in.InstallationID, repo)
		if isNotFound(err) {
			writeError(w, http.StatusConflict, "no GitHub App is set up")
			return
		} else if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch = protocol.DefaultDeployBranch
	}
	if err := protocol.ValidateBranch(branch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	userID := userFrom(r).ID
	site, err = a.store.SetSiteRepo(r.Context(), userID, site.ID, repo, branch, a.cfg.Secrets.Seal([]byte(newToken())))
	if err != nil {
		internalError(w, err)
		return
	}
	if err := a.store.SetSiteGitHubSource(r.Context(), userID, site.ID, in.InstallationID); err != nil {
		internalError(w, err)
		return
	}
	site.GitHubInstallationID = in.InstallationID
	if protocol.RepoUsesSSH(repo) && site.DeployKey == "" {
		if _, err := a.jobs.Enqueue(r.Context(), userID, site.ServerID, store.SiteRef(site.ID), protocol.ActionSiteDeployKey, a.siteParams(site)); err != nil {
			internalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, a.viewSite(site))
}

func (a *API) handleWebhookSecret(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	sealed, err := a.store.SiteWebhookSecret(r.Context(), site.ID)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "set the repository first")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	secret, err := a.cfg.Secrets.Open(sealed)
	if err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"secret": string(secret)})
}

func (a *API) handleSetDeployScript(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Script         *string `json:"script"` // null goes back to the default
		TimeoutMinutes int     `json:"timeout_minutes"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.TimeoutMinutes == 0 {
		in.TimeoutMinutes = site.DeployTimeout
	}
	if in.TimeoutMinutes < 1 || in.TimeoutMinutes > protocol.MaxDeployTimeout {
		writeError(w, http.StatusBadRequest, "the timeout must be 1 to "+strconv.Itoa(protocol.MaxDeployTimeout)+" minutes")
		return
	}
	if in.Script != nil {
		script := strings.ReplaceAll(*in.Script, "\r\n", "\n")
		if len(script) > protocol.MaxDeployScript {
			writeError(w, http.StatusBadRequest, "the deploy script is too long")
			return
		}
		in.Script = &script
	}
	site, err := a.store.SetSiteDeployScript(r.Context(), userFrom(r).ID, site.ID, in.Script, in.TimeoutMinutes)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a.viewSite(site))
}

// startDeploy queues a deploy, or explains why the site can't deploy yet.
func (a *API) startDeploy(ctx context.Context, site store.Site, trigger, sha, message string) (store.Deployment, int, string) {
	switch {
	case site.RepoURL == "":
		return store.Deployment{}, http.StatusConflict, "set the repository first"
	case protocol.RepoUsesSSH(site.RepoURL) && site.DeployKey == "":
		return store.Deployment{}, http.StatusConflict, "the server hasn't made the deploy key yet; try again in a moment"
	case site.Status == store.SiteDeleting:
		return store.Deployment{}, http.StatusConflict, "this site is being deleted"
	}
	p := a.deployParams(site)
	if err := p.Validate(); err != nil {
		return store.Deployment{}, http.StatusConflict, err.Error()
	}
	job, err := a.jobs.Enqueue(ctx, site.UserID, site.ServerID, store.SiteRef(site.ID), protocol.ActionSiteDeploy, p)
	if err != nil {
		log.Printf("internal error: %v", err)
		return store.Deployment{}, http.StatusInternalServerError, "internal error"
	}
	d, err := a.store.CreateDeployment(ctx, site.ID, job.ID, trigger, sha, message)
	if err != nil {
		log.Printf("internal error: %v", err)
		return store.Deployment{}, http.StatusInternalServerError, "internal error"
	}
	return d, 0, ""
}

func (a *API) handleDeploy(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	d, status, msg := a.startDeploy(r.Context(), site, "manual", "", "")
	if status != 0 {
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

func (a *API) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	list, err := a.store.ListDeployments(r.Context(), userFrom(r).ID, site.ID, 30)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGitHubWebhook deploys a site when GitHub reports a push to its branch.
// The request is trusted only if it is signed with the site's webhook secret.
func (a *API) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown webhook")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	sealed, err := a.store.SiteWebhookSecret(r.Context(), id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "unknown webhook")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	secret, err := a.cfg.Secrets.Open(sealed)
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
	// GitHub sends JSON, or a form with the JSON in "payload".
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		form, err := url.ParseQuery(string(body))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad form body")
			return
		}
		body = []byte(form.Get("payload"))
	}
	var push struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		HeadCommit *struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"head_commit"`
	}
	if err := json.Unmarshal(body, &push); err != nil {
		writeError(w, http.StatusBadRequest, "bad push payload")
		return
	}
	site, err := a.store.GetSiteByID(r.Context(), id)
	if err != nil {
		internalError(w, err)
		return
	}
	if push.Ref != "refs/heads/"+site.Branch {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "push to " + push.Ref + ", not " + site.Branch})
		return
	}
	if push.Deleted {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored", "reason": "the branch was deleted"})
		return
	}
	sha, message := push.After, ""
	if push.HeadCommit != nil {
		sha, message = push.HeadCommit.ID, push.HeadCommit.Message
	}
	message, _, _ = strings.Cut(message, "\n")
	d, status, msg := a.startDeploy(r.Context(), site, "push", truncate(sha, 64), truncate(message, 500))
	if status != 0 {
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "deploying", "deployment": d.ID})
}

func validSignature(secret, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// deployJobFinished records what deploy jobs produced.
func (a *API) deployJobFinished(ctx context.Context, job store.Job) {
	if job.SiteID == nil || len(job.Result) == 0 {
		return
	}
	var err error
	switch job.Action {
	case protocol.ActionSiteDeployKey:
		var res protocol.DeployKeyResult
		if json.Unmarshal(job.Result, &res) == nil && strings.HasPrefix(res.PublicKey, "ssh-ed25519 ") && len(res.PublicKey) < 512 {
			err = a.store.SetSiteDeployKey(ctx, *job.SiteID, res.PublicKey)
		} else {
			err = errors.New("the agent returned no usable public key")
		}
	case protocol.ActionSiteDeploy:
		var res protocol.DeployResult
		if json.Unmarshal(job.Result, &res) == nil && res.Commit != "" {
			err = a.store.SetDeploymentCommit(ctx, job.ID, truncate(res.Commit, 64), truncate(res.Message, 500))
		}
	}
	if err != nil {
		log.Printf("recording result of job %d: %v", job.ID, err)
	}
}

// checkAppRepo makes sure the installation can read the repository, and
// returns its https URL with GitHub's spelling of the name.
func (a *API) checkAppRepo(ctx context.Context, userID, installationID int64, repoURL string) (string, error) {
	repos, err := a.appRepos(ctx, userID)
	if err != nil {
		return "", err
	}
	want := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(repoURL, protocol.GitHubSSHPrefix), protocol.GitHubHTTPSPrefix), ".git")
	for _, r := range repos {
		if r.InstallationID == installationID && strings.EqualFold(r.FullName, want) {
			return protocol.NormalizeRepoURL(protocol.GitHubHTTPSPrefix + r.FullName)
		}
	}
	return "", errors.New("the GitHub App can't read " + want + "; install it on that repository first")
}
