package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arman-arif/control-portal/panel/hub"
	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
)

// newTestServer starts the API against a fresh schema in the database named
// by KEEL_TEST_DATABASE_URL, or skips the test when it is not set.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dbURL := os.Getenv("KEEL_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("KEEL_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("keel_test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})

	sep := "?"
	if strings.Contains(dbURL, "?") {
		sep = "&"
	}
	st, err := store.Open(ctx, dbURL+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	a := New(Config{PublicURL: "http://panel.test", WebDir: t.TempDir(), AgentDir: t.TempDir()}, st, hub.New())
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return srv
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Keel-Request", "1")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestAuthFlow(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv.URL)

	var status map[string]bool
	c.do("GET", "/api/auth/status", nil, &status)
	if !status["needs_setup"] || !status["signup_open"] {
		t.Fatalf("fresh panel status = %v", status)
	}
	if code := c.do("GET", "/api/auth/me", nil, nil); code != 401 {
		t.Fatalf("me before login = %d", code)
	}
	if code := c.do("POST", "/api/auth/signup", credentials{"bad", "password123"}, nil); code != 400 {
		t.Fatalf("signup bad email = %d", code)
	}
	if code := c.do("POST", "/api/auth/signup", credentials{"Owner@Example.com", "short"}, nil); code != 400 {
		t.Fatalf("signup short password = %d", code)
	}
	if code := c.do("POST", "/api/auth/signup", credentials{"Owner@Example.com", "password123"}, nil); code != 201 {
		t.Fatalf("signup = %d", code)
	}
	var me store.User
	if code := c.do("GET", "/api/auth/me", nil, &me); code != 200 || me.Email != "owner@example.com" {
		t.Fatalf("me = %d %+v", code, me)
	}

	other := newClient(t, srv.URL)
	if code := other.do("POST", "/api/auth/signup", credentials{"second@example.com", "password123"}, nil); code != 403 {
		t.Fatalf("second signup should be closed, got %d", code)
	}

	if code := c.do("POST", "/api/auth/logout", nil, nil); code != 204 {
		t.Fatalf("logout = %d", code)
	}
	if code := c.do("GET", "/api/auth/me", nil, nil); code != 401 {
		t.Fatalf("me after logout = %d", code)
	}
	if code := c.do("POST", "/api/auth/login", credentials{"owner@example.com", "wrong-password"}, nil); code != 401 {
		t.Fatalf("wrong password = %d", code)
	}
	if code := c.do("POST", "/api/auth/login", credentials{"OWNER@example.com", "password123"}, nil); code != 200 {
		t.Fatalf("login = %d", code)
	}

	// Lockout after repeated failures.
	for range 5 {
		other.do("POST", "/api/auth/login", credentials{"owner@example.com", "nope-nope"}, nil)
	}
	if code := other.do("POST", "/api/auth/login", credentials{"owner@example.com", "password123"}, nil); code != 429 {
		t.Fatalf("login after lockout = %d", code)
	}
}

func TestCSRFHeaderRequired(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Post(srv.URL+"/api/auth/signup", "application/json", strings.NewReader(`{"email":"a@b.co","password":"password123"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("signup without header = %d, want 403", resp.StatusCode)
	}
}

// fakeAgent registers with an install token and connects like keel-agent.
type fakeAgent struct {
	conn *websocket.Conn
}

func registerAgent(t *testing.T, base, token string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"token": token, "hostname": "web-1"})
	resp, err := http.Post(base+"/api/agent/register", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Secret string `json:"secret"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Secret
}

func connectAgent(t *testing.T, base, secret string) *fakeAgent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/agent/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return &fakeAgent{conn: conn}
}

func (f *fakeAgent) send(t *testing.T, typ, id string, v any) {
	t.Helper()
	b, _ := protocol.Encode(typ, id, v)
	if err := f.conn.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestServerLinkStatsAndServices(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/auth/signup", credentials{"owner@example.com", "password123"}, nil)

	var created struct {
		Server  serverView  `json:"server"`
		Install installInfo `json:"install"`
	}
	if code := c.do("POST", "/api/servers", map[string]string{"name": "Web 1"}, &created); code != 201 {
		t.Fatalf("create server = %d", code)
	}
	if !strings.HasPrefix(created.Install.Command, "curl -fsSL http://panel.test/install.sh | sudo bash -s -- ") {
		t.Fatalf("install command = %q", created.Install.Command)
	}
	token := created.Install.Command[strings.LastIndex(created.Install.Command, " ")+1:]
	id := created.Server.ID

	if code, _ := registerAgent(t, srv.URL, "not-a-real-token"); code != 401 {
		t.Fatalf("register with bad token = %d", code)
	}
	code, secret := registerAgent(t, srv.URL, token)
	if code != 200 || secret == "" {
		t.Fatalf("register = %d", code)
	}
	if code, _ := registerAgent(t, srv.URL, token); code != 401 {
		t.Fatalf("token reuse = %d, want 401", code)
	}
	if code := c.do("POST", fmt.Sprintf("/api/servers/%d/install-command", id), nil, nil); code != 409 {
		t.Fatalf("new install command for linked server = %d", code)
	}

	agent := connectAgent(t, srv.URL, secret)
	agent.send(t, protocol.TypeHello, "", protocol.Hello{Hostname: "web-1", OS: "Ubuntu 24.04 LTS", Arch: "amd64", CPUs: 2, AgentVersion: "test"})
	agent.send(t, protocol.TypeServices, "", []protocol.Service{
		{Name: "nginx", Active: "active", Sub: "running"},
		{Name: "evil", Active: "active", Sub: "running"},
	})
	agent.send(t, protocol.TypeStats, "", protocol.Stats{Time: 1, CPUPercent: 12.5, MemTotal: 100, MemUsed: 40})

	eventually(t, func() bool {
		var list []serverView
		c.do("GET", "/api/servers", nil, &list)
		return len(list) == 1 && list[0].Online && list[0].Latest != nil && list[0].OS == "Ubuntu 24.04 LTS"
	})

	var snap hub.Snapshot
	c.do("GET", fmt.Sprintf("/api/servers/%d/stats", id), nil, &snap)
	if len(snap.Services) != 1 || snap.Services[0].Name != "nginx" {
		t.Fatalf("services = %+v, want only nginx", snap.Services)
	}

	// Another user cannot see the server.
	if code := newClient(t, srv.URL).do("GET", fmt.Sprintf("/api/servers/%d", id), nil, nil); code != 401 {
		t.Fatalf("anonymous get = %d", code)
	}

	// Rejected before reaching the agent.
	if code := c.do("POST", fmt.Sprintf("/api/servers/%d/services/foo/restart", id), nil, nil); code != 400 {
		t.Fatalf("unmanaged service = %d", code)
	}
	if code := c.do("POST", fmt.Sprintf("/api/servers/%d/services/nginx/exec", id), nil, nil); code != 400 {
		t.Fatalf("bad action = %d", code)
	}

	// Round trip a restart job through the fake agent.
	go func() {
		_, data, err := agent.conn.Read(context.Background())
		if err != nil {
			return
		}
		var env protocol.Envelope
		json.Unmarshal(data, &env)
		var job protocol.Job
		json.Unmarshal(env.Data, &job)
		ok := env.Type == protocol.TypeJob && job.Action == protocol.ActionServiceRestart && job.Service == "nginx"
		b, _ := protocol.Encode(protocol.TypeJobResult, env.ID, protocol.JobResult{OK: ok})
		agent.conn.Write(context.Background(), websocket.MessageText, b)
	}()
	var res protocol.JobResult
	if code := c.do("POST", fmt.Sprintf("/api/servers/%d/services/nginx/restart", id), nil, &res); code != 200 || !res.OK {
		t.Fatalf("restart = %d %+v", code, res)
	}

	// Deleting the server drops the agent and revokes its secret.
	if code := c.do("DELETE", fmt.Sprintf("/api/servers/%d", id), nil, nil); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if _, _, err := agent.conn.Read(context.Background()); err == nil {
		t.Fatal("agent connection should be closed after delete")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/agent/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}},
	})
	if err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("reconnect after delete: err=%v resp=%v", err, resp)
	}
}
