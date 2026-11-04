package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

type jobView struct {
	store.Job
	Logs []store.JobLogLine `json:"logs"`
}

func (f *fakeAgent) readJob(t *testing.T) (string, protocol.Job) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := f.conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	var job protocol.Job
	json.Unmarshal(data, &env)
	json.Unmarshal(env.Data, &job)
	if env.Type != protocol.TypeJob {
		t.Fatalf("got %s message, want job", env.Type)
	}
	return env.ID, job
}

func TestQueuedJobs(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/auth/signup", credentials{"owner@example.com", "password123"}, nil)
	var created struct {
		Server  serverView  `json:"server"`
		Install installInfo `json:"install"`
	}
	c.do("POST", "/api/servers", map[string]string{"name": "Web 1"}, &created)
	id := created.Server.ID
	refresh := fmt.Sprintf("/api/servers/%d/packages/refresh", id)

	if code := c.do("POST", refresh, nil, nil); code != 409 {
		t.Fatalf("refresh on unlinked server = %d", code)
	}
	token := created.Install.Command[strings.LastIndex(created.Install.Command, " ")+1:]
	_, secret := registerAgent(t, srv.URL, token)

	// Jobs queue while the server is offline.
	var first, second store.Job
	if code := c.do("POST", refresh, nil, &first); code != 202 || first.Status != store.JobQueued {
		t.Fatalf("enqueue = %d %+v", code, first)
	}
	c.do("POST", refresh, nil, &second)

	// The first one starts when the agent connects; the second waits.
	agent := connectAgent(t, srv.URL, secret)
	envID, job := agent.readJob(t)
	if envID != fmt.Sprintf("j%d", first.ID) || job.Action != protocol.ActionPackagesRefresh {
		t.Fatalf("dispatched %s %+v", envID, job)
	}
	getJob := func(id int64, after int64) jobView {
		var v jobView
		c.do("GET", fmt.Sprintf("/api/jobs/%d?after=%d", id, after), nil, &v)
		return v
	}
	if v := getJob(second.ID, 0); v.Status != store.JobQueued {
		t.Fatalf("second job = %s, want queued", v.Status)
	}

	agent.send(t, protocol.TypeJobLog, envID, protocol.JobLog{Text: "hello "})
	agent.send(t, protocol.TypeJobLog, fmt.Sprintf("j%d", second.ID), protocol.JobLog{Text: "not running"})
	agent.send(t, protocol.TypeJobLog, envID, protocol.JobLog{Text: "world"})
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})

	envID2, _ := agent.readJob(t)
	if envID2 != fmt.Sprintf("j%d", second.ID) {
		t.Fatalf("second dispatch = %s", envID2)
	}
	v := getJob(first.ID, 0)
	var out strings.Builder
	for _, l := range v.Logs {
		out.WriteString(l.Text)
	}
	if v.Status != store.JobSucceeded || out.String() != "hello world" || v.StartedAt == nil || v.FinishedAt == nil {
		t.Fatalf("first job = %+v logs %q", v.Job, out.String())
	}
	if rest := getJob(first.ID, v.Logs[0].ID); len(rest.Logs) != 1 || rest.Logs[0].Text != "world" {
		t.Fatalf("logs after %d = %+v", v.Logs[0].ID, rest.Logs)
	}

	var list []store.Job
	c.do("GET", fmt.Sprintf("/api/servers/%d/jobs", id), nil, &list)
	if len(list) != 2 || list[0].ID != second.ID || list[0].Status != store.JobRunning {
		t.Fatalf("job list = %+v", list)
	}

	// A running job fails when its agent goes away.
	agent.conn.CloseNow()
	eventually(t, func() bool {
		v := getJob(second.ID, 0)
		return v.Status == store.JobFailed && strings.Contains(v.Error, "disconnected")
	})

	if code := newClient(t, srv.URL).do("GET", fmt.Sprintf("/api/jobs/%d", first.ID), nil, nil); code != 401 {
		t.Fatalf("anonymous job read = %d", code)
	}
	if code := c.do("GET", "/api/jobs/999999", nil, nil); code != 404 {
		t.Fatalf("missing job = %d", code)
	}
}
