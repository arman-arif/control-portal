package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/arman-arif/control-portal/protocol"
)

func TestParseInventory(t *testing.T) {
	out := strings.Join([]string{
		"nginx\tii \t1.24.0-2ubuntu7.1",
		"mysql-server\tun \t",
		"mariadb-server\tii \t1:10.11.8-0ubuntu0.24.04.1",
		"redis-server\tii \t5:7.0.15-1ubuntu0.24.04.1",
		"nodejs\tii \t22.11.0-1nodesource1",
		"certbot\trc \t2.9.0-1", // removed, config files left
		"php8.3-fpm\tii \t8.3.6-0ubuntu0.24.04.5",
		"php7.4-fpm\tii \t1:7.4.33-18+ubuntu24.04.1+deb.sury.org+1",
		"php8.1-fpm\tiU \t8.1.2", // unpacked, not configured
		"php-fpm\tii \t2:8.3+93ubuntu2",
		"garbage line",
		"",
	}, "\n")
	got := parseInventory(out)
	want := protocol.Inventory{Nginx: "1.24.0", MariaDB: "10.11.8", Redis: "7.0.15", NodeJS: "22.11.0", PHP: []string{"7.4", "8.3"}}
	if got.Nginx != want.Nginx || got.MySQL != "" || got.MariaDB != want.MariaDB || got.Redis != want.Redis ||
		got.NodeJS != want.NodeJS || got.Certbot != "" || !slices.Equal(got.PHP, want.PHP) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestUpstreamVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.24.0-2ubuntu7.1":              "1.24.0",
		"1:10.11.8-0ubuntu0.24.04.1":     "10.11.8",
		"18.19.1+dfsg-6ubuntu5":          "18.19.1",
		"8.0.40-0ubuntu0.24.04.1":        "8.0.40",
		"2.9.0":                          "2.9.0",
		"7.4.33-18+ubuntu24.04.1+deb.su": "7.4.33",
		"1.2~rc1-1":                      "1.2",
	} {
		if got := upstreamVersion(in); got != want {
			t.Errorf("upstreamVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func commands(steps []step) []string {
	var out []string
	for _, s := range steps {
		if s.args != nil {
			out = append(out, strings.Join(s.args, " "))
		} else {
			out = append(out, "fn: "+s.title)
		}
	}
	return out
}

func TestStackRecipes(t *testing.T) {
	for _, c := range protocol.Components {
		p := protocol.StackInstall{Component: c}
		switch c {
		case protocol.ComponentPHP:
			p.Version = "8.3"
		case protocol.ComponentNodeJS:
			p.Version = "22"
		}
		steps := stackRecipe(p)
		if len(steps) == 0 {
			t.Fatalf("%s: no recipe", c)
		}
		last := steps[len(steps)-1]
		if last.args == nil || last.args[0] != "apt-get" || last.args[1] != "install" {
			t.Fatalf("%s: last step is %q, want an apt-get install", c, last.title)
		}
		if !slices.Contains(last.args, "DPkg::Lock::Timeout=600") {
			t.Errorf("%s: install does not wait for the dpkg lock", c)
		}
		if strings.Contains(last.title, "Dpkg::") {
			t.Errorf("%s: title shows the dpkg options: %q", c, last.title)
		}
	}

	php := commands(stackRecipe(protocol.StackInstall{Component: protocol.ComponentPHP, Version: "8.3"}))
	if !slices.Contains(php, "add-apt-repository -y --no-update ppa:ondrej/php") {
		t.Fatalf("php recipe does not add the PPA: %q", php)
	}
	install := php[len(php)-1]
	for _, pkg := range []string{"php8.3-fpm", "php8.3-cli", "php8.3-mysql", "php8.3-redis"} {
		if !strings.Contains(install, " "+pkg) {
			t.Errorf("php install misses %s: %q", pkg, install)
		}
	}
	if strings.Contains(install, "json") {
		t.Errorf("php 8.3 install asks for a json package: %q", install)
	}
	if old := commands(stackRecipe(protocol.StackInstall{Component: protocol.ComponentPHP, Version: "7.4"})); !strings.Contains(old[len(old)-1], "php7.4-json") {
		t.Errorf("php 7.4 install misses php7.4-json")
	}

	node := commands(stackRecipe(protocol.StackInstall{Component: protocol.ComponentNodeJS, Version: "24"}))
	if !slices.Contains(node, "fn: add the NodeSource Node.js 24 repository") {
		t.Fatalf("node recipe = %q", node)
	}
}

func TestNodeSourceFiles(t *testing.T) {
	const key = "-----BEGIN PGP PUBLIC KEY BLOCK-----\nabc\n-----END PGP PUBLIC KEY BLOCK-----\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.Write([]byte("<html>not a key</html>"))
			return
		}
		w.Write([]byte(key))
	}))
	defer srv.Close()
	oldDir, oldURL := aptDir, nodeSourceKeyURL
	t.Cleanup(func() { aptDir, nodeSourceKeyURL = oldDir, oldURL })
	aptDir = t.TempDir()

	nodeSourceKeyURL = srv.URL + "/bad"
	if err := installNodeSourceKey(context.Background()); err == nil {
		t.Fatal("accepted a download that is not a key")
	}
	nodeSourceKeyURL = srv.URL + "/key"
	if err := installNodeSourceKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := writeNodeSourceRepo("24"); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(aptDir, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if read("keyrings/nodesource.asc") != key {
		t.Fatal("key not saved as downloaded")
	}
	wantList := "deb [signed-by=" + filepath.Join(aptDir, "keyrings/nodesource.asc") + "] https://deb.nodesource.com/node_24.x nodistro main\n"
	if got := read("sources.list.d/nodesource.list"); got != wantList {
		t.Fatalf("list = %q, want %q", got, wantList)
	}
	if got := read("preferences.d/nodesource"); !strings.Contains(got, "Pin: origin deb.nodesource.com") {
		t.Fatalf("pin = %q", got)
	}
}

type logCapture struct {
	mu   sync.Mutex
	text strings.Builder
}

func (c *logCapture) send(typ, id string, v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := v.(protocol.JobLog); ok && typ == protocol.TypeJobLog {
		c.text.WriteString(l.Text)
	}
	return nil
}

func TestRunSteps(t *testing.T) {
	var logs logCapture
	ran := false
	res := runSteps(context.Background(), logs.send, "j1", []step{
		cmdStep("sh", "-c", "echo one"),
		{title: "go code", fn: func(context.Context) error { ran = true; return nil }},
		cmdStep("sh", "-c", "echo env=$DEBIAN_FRONTEND"),
	})
	got := logs.text.String()
	if !res.OK || !ran {
		t.Fatalf("res = %+v, fn ran = %v", res, ran)
	}
	for _, want := range []string{"==> sh -c echo one\none\n", "==> go code\n", "env=noninteractive\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q misses %q", got, want)
		}
	}

	logs = logCapture{}
	res = runSteps(context.Background(), logs.send, "j1", []step{
		cmdStep("sh", "-c", "exit 4"),
		cmdStep("sh", "-c", "echo never"),
	})
	if res.OK || res.Error != "sh -c exit 4: command exited with status 4" || strings.Contains(logs.text.String(), "never") {
		t.Fatalf("failing command: %+v, log %q", res, logs.text.String())
	}

	logs = logCapture{}
	res = runSteps(context.Background(), logs.send, "j1", []step{
		{title: "write file", fn: func(context.Context) error { return errors.New("disk full") }},
		cmdStep("sh", "-c", "echo never"),
	})
	if res.OK || res.Error != "write file: disk full" || !strings.Contains(logs.text.String(), "error: disk full") || strings.Contains(logs.text.String(), "never") {
		t.Fatalf("failing fn: %+v, log %q", res, logs.text.String())
	}
}

func TestRunStackInstallRejectsBadParams(t *testing.T) {
	var logs logCapture
	for _, raw := range []string{`not json`, `{"component":"apache2"}`, `{"component":"php","version":"5.6"}`} {
		res := runQueuedJob(context.Background(), logs.send, "j1", protocol.Job{Action: protocol.ActionStackInstall, Params: json.RawMessage(raw)})
		if res.OK || res.Error == "" {
			t.Fatalf("%s: %+v", raw, res)
		}
	}
	if logs.text.Len() != 0 {
		t.Fatalf("rejected jobs ran something: %q", logs.text.String())
	}
}
