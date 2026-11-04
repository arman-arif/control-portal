package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

type siteResponse struct {
	Site siteView  `json:"site"`
	Job  store.Job `json:"job"`
}

func TestSites(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/auth/signup", credentials{"owner@example.com", "password123"}, nil)
	var created struct {
		Server  serverView  `json:"server"`
		Install installInfo `json:"install"`
	}
	c.do("POST", "/api/servers", map[string]string{"name": "Web 1"}, &created)
	sites := fmt.Sprintf("/api/servers/%d/sites", created.Server.ID)

	newSite := map[string]any{"domain": "Example.com", "aliases": []string{"WWW.example.com", " www.example.com "}, "php_version": "8.3"}
	if code := c.do("POST", sites, newSite, nil); code != 409 {
		t.Fatalf("site on unlinked server = %d", code)
	}
	token := created.Install.Command[strings.LastIndex(created.Install.Command, " ")+1:]
	_, secret := registerAgent(t, srv.URL, token)
	agent := connectAgent(t, srv.URL, secret)
	agent.send(t, protocol.TypeInventory, "", protocol.Inventory{Nginx: "1.24.0", PHP: []string{"8.3"}})
	eventually(t, func() bool {
		var snap struct{ Inventory *protocol.Inventory }
		c.do("GET", fmt.Sprintf("/api/servers/%d/stats", created.Server.ID), nil, &snap)
		return snap.Inventory != nil
	})

	for _, bad := range []map[string]any{
		{"domain": "localhost", "php_version": "8.3"},
		{"domain": "example.com", "php_version": "5.6"},
		{"domain": "example.com", "php_version": "8.3", "web_dir": "../../etc"},
		{"domain": "example.com", "php_version": "8.3", "aliases": []string{"example.com"}},
	} {
		if code := c.do("POST", sites, bad, nil); code != 400 {
			t.Fatalf("%v = %d, want 400", bad, code)
		}
	}
	if code := c.do("POST", sites, map[string]any{"domain": "example.com", "php_version": "8.4"}, nil); code != 409 {
		t.Fatalf("PHP version not installed = %d", code)
	}

	// Create: the site waits as "creating" and the agent gets its params.
	var first siteResponse
	if code := c.do("POST", sites, newSite, &first); code != 202 {
		t.Fatalf("create = %d", code)
	}
	if first.Site.Domain != "example.com" || first.Site.SystemUser != "k_example_com" || first.Site.Status != store.SiteCreating ||
		first.Site.WebDir != "public" || first.Site.Root != "/home/k_example_com/example.com/public" ||
		len(first.Site.Aliases) != 1 || first.Site.Aliases[0] != "www.example.com" {
		t.Fatalf("site = %+v", first.Site)
	}
	envID, job := agent.readJob(t)
	var params protocol.Site
	json.Unmarshal(job.Params, &params)
	if job.Action != protocol.ActionSiteCreate || params.Domain != "example.com" || params.SystemUser != "k_example_com" || params.PHPVersion != "8.3" {
		t.Fatalf("dispatched %+v params %+v", job, params)
	}

	// Names already served by another site are refused.
	for _, clash := range []map[string]any{
		{"domain": "example.com", "php_version": "8.3"},
		{"domain": "www.example.com", "php_version": "8.3"},
		{"domain": "shop.example.com", "aliases": []string{"www.example.com"}, "php_version": "8.3"},
	} {
		if code := c.do("POST", sites, clash, nil); code != 409 {
			t.Fatalf("%v = %d, want 409", clash, code)
		}
	}

	// Two domains with the same user base get different system users.
	var long1, long2 siteResponse
	c.do("POST", sites, map[string]any{"domain": "abcdefghijklmnopqrstuvwx.com", "php_version": "8.3"}, &long1)
	c.do("POST", sites, map[string]any{"domain": "abcdefghijklmnopqrstuvwx.org", "php_version": "8.3"}, &long2)
	if long1.Site.SystemUser != "k_abcdefghijklmnopqrstuvwx" || long2.Site.SystemUser != "k_abcdefghijklmnopqrstuvwx_2" {
		t.Fatalf("system users %q and %q", long1.Site.SystemUser, long2.Site.SystemUser)
	}

	getSite := func(id int64) (int, siteView) {
		var v siteView
		code := c.do("GET", fmt.Sprintf("/api/sites/%d", id), nil, &v)
		return code, v
	}

	// The site follows its job: success makes it active, failure records why.
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	eventually(t, func() bool { _, v := getSite(first.Site.ID); return v.Status == store.SiteActive })
	envID, _ = agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{Error: "nginx -t rejected the new config"})
	eventually(t, func() bool {
		_, v := getSite(long1.Site.ID)
		return v.Status == store.SiteFailed && v.Error == "nginx -t rejected the new config"
	})
	envID, _ = agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})

	// Update.
	site := fmt.Sprintf("/api/sites/%d", first.Site.ID)
	if code := c.do("PATCH", site, map[string]any{"domain": "other.com"}, nil); code != 400 {
		t.Fatalf("changing the domain = %d", code)
	}
	var updated siteResponse
	if code := c.do("PATCH", site, map[string]any{"aliases": []string{}, "web_dir": "web/dist"}, &updated); code != 202 {
		t.Fatalf("update = %d", code)
	}
	if updated.Site.Status != store.SiteUpdating || len(updated.Site.Aliases) != 0 || updated.Site.WebDir != "web/dist" || updated.Site.PHPVersion != "8.3" {
		t.Fatalf("updated = %+v", updated.Site)
	}
	envID, job = agent.readJob(t)
	json.Unmarshal(job.Params, &params)
	if job.Action != protocol.ActionSiteUpdate || params.WebDir != "web/dist" {
		t.Fatalf("dispatched %+v", job)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	eventually(t, func() bool { _, v := getSite(first.Site.ID); return v.Status == store.SiteActive })

	// Another user can't see or touch the site.
	other := newClient(t, srv.URL)
	other.do("POST", "/api/auth/signup", credentials{"other@example.com", "password123"}, nil)
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		if code := other.do(method, site, map[string]any{}, nil); code != 401 && code != 404 {
			t.Fatalf("%s by another user = %d", method, code)
		}
	}

	// Delete: the panel forgets the site once the server has removed it.
	var deleting siteResponse
	if code := c.do("DELETE", site+"?delete_files=true", nil, &deleting); code != 202 || deleting.Site.Status != store.SiteDeleting {
		t.Fatalf("delete = %d %+v", code, deleting.Site)
	}
	if code := c.do("PATCH", site, map[string]any{"web_dir": "public"}, nil); code != 409 {
		t.Fatalf("update while deleting = %d", code)
	}
	envID, job = agent.readJob(t)
	json.Unmarshal(job.Params, &params)
	if job.Action != protocol.ActionSiteDelete || !params.DeleteFiles {
		t.Fatalf("dispatched %+v params %+v", job, params)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	eventually(t, func() bool { code, _ := getSite(first.Site.ID); return code == 404 })

	var list []siteView
	c.do("GET", sites, nil, &list)
	if len(list) != 2 {
		t.Fatalf("sites left = %+v", list)
	}
	// The site's jobs stay in the server's history.
	var history []store.Job
	c.do("GET", fmt.Sprintf("/api/servers/%d/jobs", created.Server.ID), nil, &history)
	if len(history) != 5 || history[0].Action != protocol.ActionSiteDelete {
		t.Fatalf("history = %+v", history)
	}
}

func TestSiteSSL(t *testing.T) {
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
	report := func(inv protocol.Inventory) {
		agent.send(t, protocol.TypeInventory, "", inv)
	}
	report(protocol.Inventory{Nginx: "1.24.0", PHP: []string{"8.3"}})
	eventually(t, func() bool {
		var snap struct{ Inventory *protocol.Inventory }
		c.do("GET", fmt.Sprintf("/api/servers/%d/stats", created.Server.ID), nil, &snap)
		return snap.Inventory != nil
	})

	var res siteResponse
	c.do("POST", fmt.Sprintf("/api/servers/%d/sites", created.Server.ID), map[string]any{"domain": "example.com", "php_version": "8.3"}, &res)
	envID, _ := agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	ssl := fmt.Sprintf("/api/sites/%d/ssl", res.Site.ID)
	site := fmt.Sprintf("/api/sites/%d", res.Site.ID)

	if code := c.do("POST", ssl, map[string]string{}, nil); code != 409 {
		t.Fatalf("SSL without Certbot = %d", code)
	}
	report(protocol.Inventory{Nginx: "1.24.0", PHP: []string{"8.3"}, Certbot: "2.9.0"})
	eventually(t, func() bool { return c.do("POST", ssl, map[string]string{"email": "not an email"}, nil) == 400 })

	var started siteResponse
	if code := c.do("POST", ssl, map[string]string{}, &started); code != 202 || started.Site.Status != store.SiteUpdating {
		t.Fatalf("issue = %d %+v", code, started.Site)
	}
	envID, job := agent.readJob(t)
	var params protocol.Site
	json.Unmarshal(job.Params, &params)
	if job.Action != protocol.ActionSiteSSLIssue || !params.SSL || params.ACME == nil || params.ACME.Email != "owner@example.com" {
		t.Fatalf("dispatched %+v params %+v", job, params)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	notAfter := time.Now().Add(89 * 24 * time.Hour).UTC().Truncate(time.Second)
	report(protocol.Inventory{Nginx: "1.24.0", PHP: []string{"8.3"}, Certbot: "2.9.0",
		Certificates: []protocol.Certificate{{Domain: "example.com", Names: []string{"example.com"}, NotAfter: notAfter}}})

	var v siteView
	eventually(t, func() bool {
		c.do("GET", site, nil, &v)
		return v.SSL && v.Status == store.SiteActive && v.Certificate != nil
	})
	if !v.Certificate.NotAfter.Equal(notAfter) || v.ACMEEmail != "owner@example.com" {
		t.Fatalf("site = %+v cert %+v", v, v.Certificate)
	}

	// Later changes keep the site on HTTPS.
	c.do("PATCH", site, map[string]any{"aliases": []string{"www.example.com"}}, nil)
	_, job = agent.readJob(t)
	json.Unmarshal(job.Params, &params)
	if !params.SSL || params.ACME == nil || len(params.Aliases) != 1 {
		t.Fatalf("update params %+v", params)
	}
}
