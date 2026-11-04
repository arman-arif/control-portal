package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func postHook(t *testing.T, base string, siteID int64, event, contentType string, body []byte, signature string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/api/hooks/github/%d", base, siteID), bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("Content-Type", contentType)
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String()
}

func TestDeploys(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/auth/signup", credentials{"owner@example.com", "password123"}, nil)
	var created struct {
		Server  serverView  `json:"server"`
		Install installInfo `json:"install"`
	}
	c.do("POST", "/api/servers", map[string]string{"name": "Web 1"}, &created)
	token := created.Install.Command[strings.LastIndex(created.Install.Command, " ")+1:]
	_, secret := registerAgent(t, srv.URL, token)
	agent := connectAgent(t, srv.URL, secret)
	agent.send(t, protocol.TypeInventory, "", protocol.Inventory{Nginx: "1.24.0", PHP: []string{"8.3"}})
	var res siteResponse
	eventually(t, func() bool {
		return c.do("POST", fmt.Sprintf("/api/servers/%d/sites", created.Server.ID), map[string]any{"domain": "example.com", "php_version": "8.3"}, &res) == 202
	})
	envID, _ := agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	site := fmt.Sprintf("/api/sites/%d", res.Site.ID)
	getSite := func() siteView {
		var v siteView
		c.do("GET", site, nil, &v)
		return v
	}

	if code := c.do("POST", site+"/deploy", nil, nil); code != 409 {
		t.Fatalf("deploy without a repository = %d", code)
	}
	for _, bad := range []string{"", "gitlab.com/a/b", "https://github.com/a", "a/b;reboot"} {
		if code := c.do("PUT", site+"/repository", map[string]string{"repo_url": bad}, nil); code != 400 {
			t.Fatalf("repo %q = %d", bad, code)
		}
	}
	if code := c.do("PUT", site+"/repository", map[string]string{"repo_url": "acme/shop", "branch": "-x"}, nil); code != 400 {
		t.Fatalf("bad branch = %d", code)
	}

	// An SSH repository gets a deploy key from the server.
	var v siteView
	if code := c.do("PUT", site+"/repository", map[string]string{"repo_url": "acme/shop"}, &v); code != 200 {
		t.Fatalf("set repository = %d", code)
	}
	if v.RepoURL != "git@github.com:acme/shop.git" || v.Branch != "main" || !strings.HasSuffix(v.WebhookURL, fmt.Sprintf("/api/hooks/github/%d", v.ID)) ||
		!v.DefaultDeployScript || v.DeployScript != protocol.DefaultDeployScriptV1 {
		t.Fatalf("site = %+v", v)
	}
	if code := c.do("POST", site+"/deploy", nil, nil); code != 409 {
		t.Fatalf("deploy before the key exists = %d", code)
	}
	envID, job := agent.readJob(t)
	if job.Action != protocol.ActionSiteDeployKey {
		t.Fatalf("job %+v", job)
	}
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleExample keel-example.com"
	result, _ := json.Marshal(protocol.DeployKeyResult{PublicKey: key})
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true, Result: result})
	eventually(t, func() bool { return getSite().DeployKey == key })

	// The deploy script and timeout.
	custom := "echo hi\r\nphp artisan up\r\n"
	c.do("PUT", site+"/deploy-script", map[string]any{"script": custom, "timeout_minutes": 30}, &v)
	if v.DeployScript != "echo hi\nphp artisan up\n" || v.DefaultDeployScript || v.DeployTimeout != 30 {
		t.Fatalf("script = %q default %v timeout %d", v.DeployScript, v.DefaultDeployScript, v.DeployTimeout)
	}
	if code := c.do("PUT", site+"/deploy-script", map[string]any{"timeout_minutes": 500}, nil); code != 400 {
		t.Fatalf("timeout 500 = %d", code)
	}

	// A manual deploy; failing doesn't make the site look broken.
	var d store.Deployment
	if code := c.do("POST", site+"/deploy", nil, &d); code != 202 || d.Trigger != "manual" || (d.Status != store.JobQueued && d.Status != store.JobRunning) {
		t.Fatalf("deploy = %d %+v", code, d)
	}
	envID, job = agent.readJob(t)
	var params protocol.Deploy
	json.Unmarshal(job.Params, &params)
	if job.Action != protocol.ActionSiteDeploy || params.RepoURL != "git@github.com:acme/shop.git" || params.Script != "echo hi\nphp artisan up\n" ||
		params.TimeoutMinutes != 30 || params.Site.SystemUser != "k_example_com" {
		t.Fatalf("deploy params %+v", params)
	}
	result, _ = json.Marshal(protocol.DeployResult{Commit: strings.Repeat("a", 40), Message: "Fix checkout"})
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{Error: "the deploy script failed with status 1", Result: result})
	var list []store.Deployment
	eventually(t, func() bool {
		c.do("GET", site+"/deployments", nil, &list)
		return len(list) == 1 && list[0].Status == store.JobFailed && list[0].CommitSHA == strings.Repeat("a", 40)
	})
	if list[0].CommitMessage != "Fix checkout" || getSite().Status != store.SiteActive {
		t.Fatalf("deployment %+v, site %s", list[0], getSite().Status)
	}

	// Webhooks need the site's signature.
	var sec struct{ Secret string }
	c.do("GET", site+"/webhook-secret", nil, &sec)
	if len(sec.Secret) < 32 {
		t.Fatalf("secret %q", sec.Secret)
	}
	push := func(ref string) []byte {
		b, _ := json.Marshal(map[string]any{"ref": ref, "after": strings.Repeat("b", 40),
			"head_commit": map[string]string{"id": strings.Repeat("b", 40), "message": "Add cart\n\nLonger text"}})
		return b
	}
	body := push("refs/heads/main")
	if code, _ := postHook(t, srv.URL, v.ID, "push", "application/json", body, ""); code != 401 {
		t.Fatalf("unsigned = %d", code)
	}
	if code, _ := postHook(t, srv.URL, v.ID, "push", "application/json", body, sign("wrong", body)); code != 401 {
		t.Fatalf("wrong secret = %d", code)
	}
	if code, _ := postHook(t, srv.URL, 999999, "push", "application/json", body, sign(sec.Secret, body)); code != 404 {
		t.Fatalf("unknown site = %d", code)
	}
	ping := []byte(`{"zen":"Keep it simple."}`)
	if code, _ := postHook(t, srv.URL, v.ID, "ping", "application/json", ping, sign(sec.Secret, ping)); code != 200 {
		t.Fatalf("ping = %d", code)
	}
	other := push("refs/heads/dev")
	if code, out := postHook(t, srv.URL, v.ID, "push", "application/json", other, sign(sec.Secret, other)); code != 202 || !strings.Contains(out, "ignored") {
		t.Fatalf("other branch = %d %s", code, out)
	}
	if code, out := postHook(t, srv.URL, v.ID, "push", "application/json", body, sign(sec.Secret, body)); code != 202 || !strings.Contains(out, "deploying") {
		t.Fatalf("push = %d %s", code, out)
	}
	_, job = agent.readJob(t)
	if job.Action != protocol.ActionSiteDeploy {
		t.Fatalf("push started %+v", job)
	}
	c.do("GET", site+"/deployments", nil, &list)
	if len(list) != 2 || list[0].Trigger != "push" || list[0].CommitSHA != strings.Repeat("b", 40) || list[0].CommitMessage != "Add cart" {
		t.Fatalf("deployments %+v", list)
	}

	// GitHub's form encoding is signed over the raw form body.
	form := []byte("payload=" + url.QueryEscape(string(body)))
	if code, out := postHook(t, srv.URL, v.ID, "push", "application/x-www-form-urlencoded", form, sign(sec.Secret, form)); code != 202 || !strings.Contains(out, "deploying") {
		t.Fatalf("form push = %d %s", code, out)
	}
}
