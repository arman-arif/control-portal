package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

func TestEnvEditor(t *testing.T) {
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
	var res siteResponse
	c.do("POST", fmt.Sprintf("/api/servers/%d/sites", created.Server.ID), map[string]any{"domain": "example.com", "php_version": "8.3"}, &res)
	envID, _ := agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	env := fmt.Sprintf("/api/sites/%d/env", res.Site.ID)
	const secretValue = "DB_PASSWORD=hunter2-very-secret"

	// The agent answers each request; the HTTP call waits for it.
	answer := func(want string, reply func(protocol.EnvParams) protocol.JobResult) chan protocol.EnvParams {
		got := make(chan protocol.EnvParams, 1)
		go func() {
			id, job := agent.readJob(t)
			var p protocol.EnvParams
			json.Unmarshal(job.Params, &p)
			if job.Action != want {
				t.Errorf("agent got %s, want %s", job.Action, want)
			}
			got <- p
			agent.send(t, protocol.TypeJobResult, id, reply(p))
		}()
		return got
	}

	got := answer(protocol.ActionSiteEnvRead, func(protocol.EnvParams) protocol.JobResult {
		result, _ := json.Marshal(protocol.EnvFile{Exists: true, Content: "APP_ENV=production\n" + secretValue + "\n"})
		return protocol.JobResult{OK: true, Result: result}
	})
	var file protocol.EnvFile
	if code := c.do("GET", env, nil, &file); code != 200 || !file.Exists || !strings.Contains(file.Content, secretValue) {
		t.Fatalf("read = %d %+v", code, file)
	}
	if p := <-got; p.Site.SystemUser != "k_example_com" || p.Content != "" {
		t.Fatalf("read params %+v", p)
	}

	got = answer(protocol.ActionSiteEnvWrite, func(protocol.EnvParams) protocol.JobResult { return protocol.JobResult{OK: true} })
	if code := c.do("PUT", env, map[string]string{"content": "APP_ENV=production\r\n" + secretValue + "\r\n"}, nil); code != 204 {
		t.Fatalf("write = %d", code)
	}
	if p := <-got; p.Content != "APP_ENV=production\n"+secretValue+"\n" {
		t.Fatalf("write params %q", p.Content)
	}
	if code := c.do("PUT", env, map[string]string{"content": strings.Repeat("x", protocol.MaxEnvFile+1)}, nil); code != 400 {
		t.Fatalf("oversized write = %d", code)
	}

	// The history shows the change but nothing of the content.
	var history []store.Job
	c.do("GET", fmt.Sprintf("/api/servers/%d/jobs", created.Server.ID), nil, &history)
	if history[0].Action != protocol.ActionSiteEnvWrite || history[0].Status != store.JobSucceeded || !strings.Contains(string(history[0].Params), `"redacted":true`) {
		t.Fatalf("history %+v", history[0])
	}
	for _, j := range history {
		var v jobView
		c.do("GET", fmt.Sprintf("/api/jobs/%d", j.ID), nil, &v)
		all, _ := json.Marshal(v)
		if strings.Contains(string(all), "hunter2") {
			t.Fatalf("job %d holds the .env content: %s", j.ID, all)
		}
	}

	// A failure on the server is passed on.
	answer(protocol.ActionSiteEnvRead, func(protocol.EnvParams) protocol.JobResult {
		return protocol.JobResult{Error: "reading .env: exit status 4 .env is not a regular file"}
	})
	if code := c.do("GET", env, nil, nil); code != 422 {
		t.Fatalf("failed read = %d", code)
	}

	agent.conn.CloseNow()
	eventually(t, func() bool { return c.do("GET", env, nil, nil) == 409 })
}
