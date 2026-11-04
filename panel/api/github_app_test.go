package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

// fakeGitHub answers the GitHub API calls Keel makes, and checks that app
// requests carry a JWT signed with the app's key.
type fakeGitHub struct {
	*httptest.Server
	key *rsa.PrivateKey

	mu          sync.Mutex
	tokenRepos  [][]string
	tokensGiven int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeGitHub{key: key}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	appJWTOK := func(r *http.Request) bool {
		jwt, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if !ok || len(parts) != 3 {
			return false
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		return rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) == nil
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/app-manifests/good-code/conversions":
			json.NewEncoder(w).Encode(map[string]any{"id": 77, "slug": "keel-test", "name": "Keel test", "html_url": "https://github.com/apps/keel-test",
				"webhook_secret": "whsec", "pem": pemText})
		case strings.HasPrefix(r.URL.Path, "/app/") && !appJWTOK(r):
			http.Error(w, `{"message":"bad JWT"}`, 401)
		case r.URL.Path == "/app/installations":
			w.Write([]byte(`[{"id":500,"account":{"login":"acme"}}]`))
		case r.URL.Path == "/app/installations/500/access_tokens":
			var body struct{ Repositories []string }
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.tokenRepos = append(f.tokenRepos, body.Repositories)
			f.tokensGiven++
			n := f.tokensGiven
			f.mu.Unlock()
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"token":"ghs_token%d"}`, n)
		case r.URL.Path == "/installation/repositories" && strings.HasPrefix(r.Header.Get("Authorization"), "token ghs_"):
			w.Write([]byte(`{"repositories":[{"full_name":"Acme/Shop","private":true,"default_branch":"main"}]}`))
		default:
			http.Error(w, `{"message":"Not Found"}`, 404)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func TestGitHubApp(t *testing.T) {
	gh := newFakeGitHub(t)
	srv := newTestServer(t, func(c *Config) { c.GitHubAPI, c.GitHubWeb, c.AllowSignup = gh.URL, "https://github.test", true })
	c := newClient(t, srv.URL)
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.do("POST", "/api/auth/signup", credentials{"owner@example.com", "password123"}, nil)

	var status struct {
		Configured    bool
		InstallURL    string `json:"install_url"`
		Installations []struct{ ID int64 }
	}
	c.do("GET", "/api/github-app", nil, &status)
	if status.Configured {
		t.Fatal("configured before anything was set up")
	}

	// The manifest the browser posts to GitHub.
	var m struct{ Action, Manifest string }
	if code := c.do("POST", "/api/github-app/manifest", map[string]string{"organization": "bad org!"}, nil); code != 400 {
		t.Fatalf("bad org = %d", code)
	}
	c.do("POST", "/api/github-app/manifest", map[string]string{"organization": "acme"}, &m)
	if !strings.HasPrefix(m.Action, "https://github.test/organizations/acme/settings/apps/new?state=") {
		t.Fatalf("action %q", m.Action)
	}
	var manifest struct {
		HookAttributes struct{ URL string } `json:"hook_attributes"`
		RedirectURL    string               `json:"redirect_url"`
		Permissions    map[string]string    `json:"default_permissions"`
		Events         []string             `json:"default_events"`
		Public         bool
	}
	json.Unmarshal([]byte(m.Manifest), &manifest)
	if manifest.HookAttributes.URL != "http://panel.test/api/hooks/github-app" || manifest.RedirectURL != "http://panel.test/api/github-app/callback" ||
		manifest.Permissions["contents"] != "read" || len(manifest.Events) != 1 || manifest.Public {
		t.Fatalf("manifest %s", m.Manifest)
	}
	u, _ := url.Parse(m.Action)
	state := u.Query().Get("state")

	// GitHub sends the browser back with a code.
	callback := func(cl *client, code, state string) string {
		req, _ := http.NewRequest("GET", srv.URL+"/api/github-app/callback?code="+code+"&state="+url.QueryEscape(state), nil)
		resp, err := cl.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.Header.Get("Location")
	}
	other := newClient(t, srv.URL)
	other.http.CheckRedirect = c.http.CheckRedirect
	other.do("POST", "/api/auth/signup", credentials{"other@example.com", "password123"}, nil)
	if loc := callback(other, "good-code", state); !strings.Contains(loc, "github_error=") {
		t.Fatalf("another account's state accepted: %q", loc)
	}
	if loc := callback(c, "good-code", "made-up"); !strings.Contains(loc, "github_error=") {
		t.Fatalf("made-up state accepted: %q", loc)
	}
	if loc := callback(c, "good-code", state); loc != "/settings?github=created" {
		t.Fatalf("callback redirected to %q", loc)
	}
	c.do("GET", "/api/github-app", nil, &status)
	if !status.Configured || status.InstallURL != "https://github.test/apps/keel-test/installations/new" || len(status.Installations) != 1 {
		t.Fatalf("status %+v", status)
	}
	var repos []appRepo
	c.do("GET", "/api/github-app/repositories", nil, &repos)
	if len(repos) != 1 || repos[0].FullName != "Acme/Shop" || repos[0].InstallationID != 500 {
		t.Fatalf("repos %+v", repos)
	}

	// A site that deploys through the app.
	var created struct {
		Server  serverView  `json:"server"`
		Install installInfo `json:"install"`
	}
	c.do("POST", "/api/servers", map[string]string{"name": "Web 1"}, &created)
	token := created.Install.Command[strings.LastIndex(created.Install.Command, " ")+1:]
	_, secret := registerAgent(t, srv.URL, token)
	agent := connectAgent(t, srv.URL, secret)
	var res siteResponse
	c.do("POST", fmt.Sprintf("/api/servers/%d/sites", created.Server.ID), map[string]any{"domain": "example.com", "php_version": "8.3"}, &res)
	envID, _ := agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	site := fmt.Sprintf("/api/sites/%d", res.Site.ID)

	if code := c.do("PUT", site+"/repository", map[string]any{"repo_url": "acme/other", "installation_id": 500}, nil); code != 400 {
		t.Fatalf("repo the app can't read = %d", code)
	}
	var v siteView
	if code := c.do("PUT", site+"/repository", map[string]any{"repo_url": "acme/shop", "installation_id": 500}, &v); code != 200 {
		t.Fatalf("set app repo = %d", code)
	}
	if v.RepoURL != "https://github.com/Acme/Shop.git" || v.GitHubInstallationID == nil || *v.GitHubInstallationID != 500 {
		t.Fatalf("site %+v", v)
	}

	var d store.Deployment
	if code := c.do("POST", site+"/deploy", nil, &d); code != 202 {
		t.Fatalf("deploy = %d", code)
	}
	envID, job := agent.readJob(t)
	var params protocol.Deploy
	json.Unmarshal(job.Params, &params)
	if job.Action != protocol.ActionSiteDeploy || !strings.HasPrefix(params.Token, "ghs_token") || params.RepoURL != "https://github.com/Acme/Shop.git" {
		t.Fatalf("deploy params %+v", params)
	}
	gh.mu.Lock()
	lastRepos := gh.tokenRepos[len(gh.tokenRepos)-1]
	gh.mu.Unlock()
	if len(lastRepos) != 1 || lastRepos[0] != "Shop" {
		t.Fatalf("token asked for %v, want only Shop", lastRepos)
	}
	var stored jobView
	c.do("GET", fmt.Sprintf("/api/jobs/%d", *d.JobID), nil, &stored)
	if strings.Contains(string(stored.Params), "ghs_") {
		t.Fatalf("the stored job has the token: %s", stored.Params)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})

	// The app's webhook deploys the sites that follow the pushed branch.
	hook := func(appID string, body []byte, sig string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/hooks/github-app", strings.NewReader(string(body)))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Hook-Installation-Target-ID", appID)
		req.Header.Set("X-Hub-Signature-256", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out strings.Builder
		b := make([]byte, 1024)
		n, _ := resp.Body.Read(b)
		out.Write(b[:n])
		return resp.StatusCode, out.String()
	}
	push := func(ref string) []byte {
		b, _ := json.Marshal(map[string]any{"ref": ref, "after": strings.Repeat("d", 40), "repository": map[string]string{"full_name": "Acme/Shop"},
			"installation": map[string]int{"id": 500}, "head_commit": map[string]string{"id": strings.Repeat("d", 40), "message": "Ship it"}})
		return b
	}
	body := push("refs/heads/main")
	if code, _ := hook("77", body, sign("wrong", body)); code != 401 {
		t.Fatalf("bad signature = %d", code)
	}
	if code, _ := hook("78", body, sign("whsec", body)); code != 404 {
		t.Fatalf("unknown app = %d", code)
	}
	other2 := push("refs/heads/dev")
	if code, out := hook("77", other2, sign("whsec", other2)); code != 202 || !strings.Contains(out, `"deploying":0`) {
		t.Fatalf("other branch = %d %s", code, out)
	}
	if code, out := hook("77", body, sign("whsec", body)); code != 202 || !strings.Contains(out, `"deploying":1`) {
		t.Fatalf("push = %d %s", code, out)
	}
	envID, job = agent.readJob(t)
	json.Unmarshal(job.Params, &params)
	if job.Action != protocol.ActionSiteDeploy || params.Token == "" {
		t.Fatalf("push deploy %+v", params)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})

	// Without the app, the site's deploys fail with a clear reason.
	c.do("DELETE", "/api/github-app", nil, nil)
	c.do("POST", site+"/deploy", nil, nil)
	var list []store.Deployment
	eventually(t, func() bool {
		c.do("GET", site+"/deployments", nil, &list)
		return len(list) > 0 && list[0].Status == store.JobFailed
	})
	if !strings.Contains(list[0].Error, "no longer set up") {
		t.Fatalf("error %q", list[0].Error)
	}
}
