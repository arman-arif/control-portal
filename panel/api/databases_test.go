package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

func TestDatabases(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/auth/signup", credentials{"owner@example.com", "password123"}, nil)
	var created struct {
		Server  serverView  `json:"server"`
		Install installInfo `json:"install"`
	}
	c.do("POST", "/api/servers", map[string]string{"name": "DB 1"}, &created)
	sid := created.Server.ID
	token := created.Install.Command[strings.LastIndex(created.Install.Command, " ")+1:]
	_, secret := registerAgent(t, srv.URL, token)
	agent := connectAgent(t, srv.URL, secret)
	dbs := fmt.Sprintf("/api/servers/%d/databases", sid)
	users := fmt.Sprintf("/api/servers/%d/database-users", sid)

	inventory := func() *protocol.Inventory {
		var snap struct{ Inventory *protocol.Inventory }
		c.do("GET", fmt.Sprintf("/api/servers/%d/stats", sid), nil, &snap)
		return snap.Inventory
	}
	agent.send(t, protocol.TypeInventory, "", protocol.Inventory{Nginx: "1.24.0"})
	eventually(t, func() bool { return inventory() != nil })
	if code := c.do("POST", dbs, map[string]any{"name": "shop"}, nil); code != 409 {
		t.Fatalf("database without MySQL = %d", code)
	}
	agent.send(t, protocol.TypeInventory, "", protocol.Inventory{MySQL: "8.0.46"})
	eventually(t, func() bool { inv := inventory(); return inv != nil && inv.MySQL != "" })
	for _, bad := range []string{"", "Shop", "my-db", "mysql", "x`;drop"} {
		if code := c.do("POST", dbs, map[string]any{"name": bad}, nil); code != 400 {
			t.Fatalf("name %q = %d", bad, code)
		}
	}

	// A database with a user of the same name.
	var made struct {
		Database store.Database `json:"database"`
		User     store.DBUser   `json:"user"`
		Password string         `json:"password"`
	}
	eventually(t, func() bool {
		return c.do("POST", dbs, map[string]any{"name": "shop", "create_user": true}, &made) == 202
	})
	if made.Database.Status != store.DBCreating || made.User.Name != "shop" || len(made.Password) != 24 || protocol.ValidateDBPassword(made.Password) != nil {
		t.Fatalf("made = %+v", made)
	}
	if code := c.do("POST", dbs, map[string]any{"name": "shop"}, nil); code != 409 {
		t.Fatalf("duplicate = %d", code)
	}
	if code := c.do("POST", users, map[string]any{"name": "shop"}, nil); code != 409 {
		t.Fatalf("duplicate user = %d", code)
	}
	if code := c.do("POST", users, map[string]any{"name": "root"}, nil); code != 400 {
		t.Fatalf("reserved user name = %d", code)
	}

	envID, job := agent.readJob(t)
	if job.Action != protocol.ActionDBCreate || string(job.Params) != `{"name":"shop"}` {
		t.Fatalf("first job %+v %s", job, job.Params)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	userEnv, job := agent.readJob(t)
	var up protocol.DBUserParams
	up = protocol.DBUserParams{}
	json.Unmarshal(job.Params, &up)
	if job.Action != protocol.ActionDBUserCreate || up.Password != made.Password || !slices.Equal(up.Databases, []string{"shop"}) {
		t.Fatalf("user job %+v", up)
	}
	// The agent got the password, but the job history doesn't have it.
	var history []store.Job
	c.do("GET", fmt.Sprintf("/api/servers/%d/jobs", sid), nil, &history)
	for _, j := range history {
		if strings.Contains(string(j.Params), made.Password) {
			t.Fatalf("job %d stores the password: %s", j.ID, j.Params)
		}
	}
	agent.send(t, protocol.TypeJobResult, userEnv, protocol.JobResult{OK: true})

	var list struct {
		Databases []store.Database `json:"databases"`
		Users     []store.DBUser   `json:"users"`
	}
	eventually(t, func() bool {
		c.do("GET", dbs, nil, &list)
		return len(list.Databases) == 1 && list.Databases[0].Status == store.DBActive && len(list.Users) == 1 && list.Users[0].Status == store.DBActive
	})
	if !slices.Equal(list.Databases[0].Users, []string{"shop"}) {
		t.Fatalf("list = %+v", list)
	}

	var revealed struct{ Password string }
	userPath := fmt.Sprintf("/api/database-users/%d", made.User.ID)
	c.do("GET", userPath+"/password", nil, &revealed)
	if revealed.Password != made.Password {
		t.Fatalf("revealed %q, want %q", revealed.Password, made.Password)
	}

	// A second database, then give the user both.
	var logs struct {
		Database store.Database `json:"database"`
	}
	c.do("POST", dbs, map[string]any{"name": "logs"}, &logs)
	envID, _ = agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})

	// Someone else's database can't be granted.
	other := newClient(t, srv.URL)
	other.do("POST", "/api/auth/signup", credentials{"other@example.com", "password123"}, nil)
	if code := other.do("GET", userPath+"/password", nil, nil); code != 401 && code != 404 {
		t.Fatalf("another account revealed the password: %d", code)
	}
	if code := c.do("PUT", userPath+"/databases", map[string]any{"database_ids": []int64{made.Database.ID, 999999}}, nil); code != 400 {
		t.Fatalf("grant unknown database = %d", code)
	}
	if code := c.do("PUT", userPath+"/databases", map[string]any{"database_ids": []int64{made.Database.ID, logs.Database.ID}}, nil); code != 202 {
		t.Fatalf("grant = %d", code)
	}
	envID, job = agent.readJob(t)
	up = protocol.DBUserParams{}
	json.Unmarshal(job.Params, &up)
	if job.Action != protocol.ActionDBUserGrants || up.Password != "" || !slices.Equal(up.Databases, []string{"logs", "shop"}) {
		t.Fatalf("grants job %s", job.Params)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})

	// A new password replaces the old one on the server and in the panel.
	var reset struct{ Password string }
	c.do("POST", userPath+"/password", nil, &reset)
	envID, job = agent.readJob(t)
	up = protocol.DBUserParams{}
	json.Unmarshal(job.Params, &up)
	if reset.Password == made.Password || up.Password != reset.Password || job.Action != protocol.ActionDBUserCreate {
		t.Fatalf("reset %q, sent %q", reset.Password, up.Password)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	c.do("GET", userPath+"/password", nil, &revealed)
	if revealed.Password != reset.Password {
		t.Fatal("reveal shows the old password")
	}

	// A failed job leaves the database marked with the error.
	c.do("DELETE", fmt.Sprintf("/api/databases/%d", logs.Database.ID), nil, nil)
	envID, job = agent.readJob(t)
	if job.Action != protocol.ActionDBDrop {
		t.Fatalf("drop job %+v", job)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{Error: "mysql: exit status 1"})
	eventually(t, func() bool {
		c.do("GET", dbs, nil, &list)
		return len(list.Databases) == 2 && list.Databases[0].Name == "logs" && list.Databases[0].Status == store.DBFailed
	})
	c.do("DELETE", fmt.Sprintf("/api/databases/%d", logs.Database.ID), nil, nil)
	envID, _ = agent.readJob(t)
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	eventually(t, func() bool {
		c.do("GET", dbs, nil, &list)
		return len(list.Databases) == 1 && slices.Equal(list.Users[0].Databases, []string{"shop"})
	})

	c.do("DELETE", userPath, nil, nil)
	envID, job = agent.readJob(t)
	if job.Action != protocol.ActionDBUserDrop {
		t.Fatalf("drop user job %+v", job)
	}
	agent.send(t, protocol.TypeJobResult, envID, protocol.JobResult{OK: true})
	eventually(t, func() bool { return c.do("GET", userPath+"/password", nil, nil) == 404 })
}
