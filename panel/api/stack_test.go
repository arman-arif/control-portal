package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

func TestStackInstall(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/auth/signup", credentials{"owner@example.com", "password123"}, nil)
	var created struct {
		Server  serverView  `json:"server"`
		Install installInfo `json:"install"`
	}
	c.do("POST", "/api/servers", map[string]string{"name": "Web 1"}, &created)
	id := created.Server.ID
	install := fmt.Sprintf("/api/servers/%d/stack/install", id)
	stats := fmt.Sprintf("/api/servers/%d/stats", id)

	for _, body := range []map[string]string{
		{"component": "apache2"},
		{"component": "php"},
		{"component": "php", "version": "5.6"},
		{"component": "nginx", "version": "1.24"},
		{"component": "nodejs", "version": "18"},
	} {
		if code := c.do("POST", install, body, nil); code != 400 {
			t.Fatalf("%v = %d, want 400", body, code)
		}
	}
	if code := c.do("POST", install, map[string]string{"component": "nginx"}, nil); code != 409 {
		t.Fatalf("install on unlinked server = %d", code)
	}

	token := created.Install.Command[strings.LastIndex(created.Install.Command, " ")+1:]
	_, secret := registerAgent(t, srv.URL, token)
	agent := connectAgent(t, srv.URL, secret)

	var snap struct {
		Inventory *protocol.Inventory `json:"inventory"`
	}
	c.do("GET", stats, nil, &snap)
	if snap.Inventory != nil {
		t.Fatalf("inventory before the agent reported one = %+v", snap.Inventory)
	}

	// The panel keeps a cleaned copy of what the agent reports.
	agent.send(t, protocol.TypeInventory, "", protocol.Inventory{MariaDB: "10.11.8", PHP: []string{"8.3", "bad value"}})
	eventually(t, func() bool {
		c.do("GET", stats, nil, &snap)
		return snap.Inventory != nil
	})
	if snap.Inventory.MariaDB != "10.11.8" || len(snap.Inventory.PHP) != 1 || snap.Inventory.PHP[0] != "8.3" {
		t.Fatalf("inventory = %+v", snap.Inventory)
	}

	// MySQL conflicts with the MariaDB the agent reported.
	if code := c.do("POST", install, map[string]string{"component": "mysql"}, nil); code != 409 {
		t.Fatalf("mysql next to mariadb = %d", code)
	}

	var job store.Job
	if code := c.do("POST", install, map[string]string{"component": "php", "version": "8.4"}, &job); code != 202 {
		t.Fatalf("install php = %d", code)
	}
	if job.Action != protocol.ActionStackInstall {
		t.Fatalf("job = %+v", job)
	}
	envID, sent := agent.readJob(t)
	var params protocol.StackInstall
	json.Unmarshal(sent.Params, &params)
	if envID != fmt.Sprintf("j%d", job.ID) || sent.Action != protocol.ActionStackInstall || params != (protocol.StackInstall{Component: "php", Version: "8.4"}) {
		t.Fatalf("dispatched %s %+v params %+v", envID, sent, params)
	}

	other := newClient(t, srv.URL)
	other.do("POST", "/api/auth/signup", credentials{"other@example.com", "password123"}, nil)
	if code := other.do("POST", install, map[string]string{"component": "nginx"}, nil); code != 401 && code != 404 {
		t.Fatalf("install on another user's server = %d", code)
	}
}
