package main

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/arman-arif/control-portal/protocol"
)

func TestParseCPULineAndPercent(t *testing.T) {
	a, err := parseCPULine("cpu  100 0 100 700 100 0 0 0 0 0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseCPULine("cpu  150 0 150 750 150 0 0 0 0 0")
	if err != nil {
		t.Fatal(err)
	}
	// delta total 200, delta idle (idle+iowait) 100 -> 50% busy
	if got := cpuPercent(a, b); got != 50 {
		t.Fatalf("cpuPercent = %v, want 50", got)
	}
	if got := cpuPercent(b, a); got != 0 {
		t.Fatalf("cpuPercent on counter reset = %v, want 0", got)
	}
	if _, err := parseCPULine("cpu0 1 2 3 4"); err == nil {
		t.Fatal("expected error for per-core line")
	}
}

func TestParseMeminfo(t *testing.T) {
	total, used, err := parseMeminfo("MemTotal:  2000 kB\nMemFree: 100 kB\nMemAvailable:  500 kB\n")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2000*1024 || used != 1500*1024 {
		t.Fatalf("got total=%d used=%d", total, used)
	}
	if _, _, err := parseMeminfo("MemTotal: 1 kB\n"); err == nil {
		t.Fatal("expected error without MemAvailable")
	}
}

func TestParseLoadAndUptime(t *testing.T) {
	l1, l5, l15, err := parseLoadavg("0.50 0.25 0.10 1/200 1234\n")
	if err != nil || l1 != 0.5 || l5 != 0.25 || l15 != 0.1 {
		t.Fatalf("loadavg = %v %v %v %v", l1, l5, l15, err)
	}
	up, err := parseUptime("3600.42 100.00\n")
	if err != nil || up != 3600 {
		t.Fatalf("uptime = %v %v", up, err)
	}
}

func TestParseServiceList(t *testing.T) {
	out := `nginx.service loaded active running A high performance web server
php8.3-fpm.service loaded active running The PHP 8.3 FastCGI Process Manager
mysql.service loaded failed failed MySQL Community Server
foo.service loaded active running Something unmanaged
redis-server.service not-found inactive dead redis-server.service
`
	got := parseServiceList(out)
	want := []protocol.Service{
		{Name: "nginx", Active: "active", Sub: "running"},
		{Name: "php8.3-fpm", Active: "active", Sub: "running"},
		{Name: "mysql", Active: "failed", Sub: "failed"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("service %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRunServiceJobRejectsUnmanaged(t *testing.T) {
	res := runServiceJob(t.Context(), protocol.Job{Action: protocol.ActionServiceRestart, Service: "sshd; rm -rf /"})
	if res.OK || res.Error == "" {
		t.Fatalf("expected rejection, got %+v", res)
	}
	res = runServiceJob(t.Context(), protocol.Job{Action: "shell.exec", Service: "nginx"})
	if res.OK || res.Error == "" {
		t.Fatalf("expected rejection, got %+v", res)
	}
}

func TestWSURL(t *testing.T) {
	cases := map[string]string{
		"https://panel.example.com": "wss://panel.example.com/api/agent/ws",
		"http://10.0.0.1:8080":      "ws://10.0.0.1:8080/api/agent/ws",
	}
	for in, want := range cases {
		if got := wsURL(in); got != want {
			t.Errorf("wsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompleteUTF8(t *testing.T) {
	s := []byte("aé€") // 1 + 2 + 3 bytes
	for n, want := range []int{0, 1, 1, 3, 3, 3, 6} {
		if got := completeUTF8(s[:n]); got != want {
			t.Errorf("completeUTF8(%q) = %d, want %d", s[:n], got, want)
		}
	}
}

func TestLogWriterChunks(t *testing.T) {
	var chunks []string
	w := newLogWriter(func(s string) { chunks = append(chunks, s) })
	// Large output with a multi-byte character straddling the chunk limit.
	in := strings.Repeat("x", protocol.MaxLogChunk-1) + "€" + strings.Repeat("y", 100)
	w.Write([]byte(in[:protocol.MaxLogChunk]))
	w.Write([]byte(in[protocol.MaxLogChunk:]))
	w.Close()
	if got := strings.Join(chunks, ""); got != in {
		t.Fatalf("output changed: got %d bytes, want %d", len(got), len(in))
	}
	for _, c := range chunks {
		if len(c) > protocol.MaxLogChunk || !utf8.ValidString(c) {
			t.Fatalf("bad chunk: %d bytes, valid=%v", len(c), utf8.ValidString(c))
		}
	}
}

func TestRunStreamed(t *testing.T) {
	var mu sync.Mutex
	var logs strings.Builder
	send := func(typ, id string, v any) error {
		mu.Lock()
		defer mu.Unlock()
		if l, ok := v.(protocol.JobLog); ok && typ == protocol.TypeJobLog && id == "j1" {
			logs.WriteString(l.Text)
		}
		return nil
	}
	res := runStreamed(context.Background(), send, "j1", exec.Command("sh", "-c", "echo out; echo err >&2"))
	if !res.OK || !strings.Contains(logs.String(), "out\n") || !strings.Contains(logs.String(), "err\n") {
		t.Fatalf("res=%+v logs=%q", res, logs.String())
	}
	res = runStreamed(context.Background(), send, "j1", exec.Command("sh", "-c", "exit 3"))
	if res.OK || res.Error != "command exited with status 3" {
		t.Fatalf("failing command: %+v", res)
	}
	if res := runQueuedJob(context.Background(), send, "j1", protocol.Job{Action: "shell.exec"}); res.OK {
		t.Fatal("unknown action must be rejected")
	}
}
