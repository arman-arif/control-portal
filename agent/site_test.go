package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// fakeSystem replaces the agent's system hooks with a temporary root, a user
// table, and recorded commands.
type fakeSystem struct {
	t           *testing.T
	root        string
	users       map[string]*user.User
	commands    []string
	failNginx   bool
	failFPM     bool
	failReload  string // service whose reload fails
	failCertbot bool
	groups      map[string]bool
}

func newFakeSystem(t *testing.T) *fakeSystem {
	f := &fakeSystem{t: t, root: t.TempDir(), users: map[string]*user.User{}, groups: map[string]bool{}}
	oldRoot, oldRun, oldLookup, oldGroup, oldWait := sysRoot, runCommand, lookupUser, lookupGroup, waitForSocket
	t.Cleanup(func() {
		sysRoot, runCommand, lookupUser, lookupGroup, waitForSocket = oldRoot, oldRun, oldLookup, oldGroup, oldWait
	})
	lookupGroup = func(name string) (*user.Group, error) {
		if f.groups[name] {
			return &user.Group{Name: name}, nil
		}
		return nil, user.UnknownGroupError(name)
	}
	waitForSocket = func(ctx context.Context, path string) error {
		f.commands = append(f.commands, "wait "+path)
		return nil
	}
	sysRoot = f.root
	for _, d := range []string{"/home", "/etc/nginx/sites-available", "/etc/nginx/sites-enabled", "/etc/php/8.3/fpm/pool.d", "/etc/php/8.4/fpm/pool.d"} {
		if err := os.MkdirAll(f.root+d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lookupUser = func(name string) (*user.User, error) {
		if u, ok := f.users[name]; ok {
			return u, nil
		}
		return nil, user.UnknownUserError(name)
	}
	runCommand = func(ctx context.Context, out io.Writer, name string, args ...string) error {
		f.commands = append(f.commands, strings.Join(append([]string{name}, args...), " "))
		switch {
		case name == "useradd":
			n := args[len(args)-1]
			home := args[slices.Index(args, "--home-dir")+1]
			f.users[n] = &user.User{Username: n, HomeDir: home, Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid())}
			f.groups[n] = true
			return os.MkdirAll(f.root+home, 0o755)
		case name == "userdel":
			delete(f.users, args[len(args)-1]) // the group stays, as when www-data is still in it
		case name == "groupdel":
			delete(f.groups, args[0])
		case name == "systemctl" && args[0] == "reload-or-restart" && args[1] == f.failReload:
			return exec.Command("false").Run()
		case name == "certbot" && args[0] == "certonly":
			if f.failCertbot {
				io.WriteString(out, "Challenge failed for domain example.com\n")
				return exec.Command("false").Run()
			}
			name := args[slices.Index(args, "--cert-name")+1]
			os.MkdirAll(f.root+"/etc/letsencrypt/live/"+name, 0o755)
			os.MkdirAll(f.root+"/etc/letsencrypt/renewal", 0o755)
			os.WriteFile(f.root+"/etc/letsencrypt/renewal/"+name+".conf", nil, 0o644)
			return os.WriteFile(f.root+"/etc/letsencrypt/live/"+name+"/fullchain.pem", []byte("cert"), 0o644)
		case name == "nginx" && f.failNginx:
			io.WriteString(out, "nginx: [emerg] something is wrong\n")
			return exec.Command("false").Run()
		case strings.HasPrefix(name, "/usr/sbin/php-fpm") && f.failFPM:
			return exec.Command("false").Run()
		}
		return nil
	}
	return f
}

func (f *fakeSystem) read(path string) string {
	f.t.Helper()
	b, err := os.ReadFile(f.root + path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *fakeSystem) exists(path string) bool {
	_, err := os.Lstat(f.root + path)
	return err == nil
}

func (f *fakeSystem) ran(cmd string) bool { return slices.Contains(f.commands, cmd) }

func (f *fakeSystem) run(steps []step) protocol.JobResult {
	var logs logCapture
	return runSteps(context.Background(), logs.send, "j1", steps)
}

var stackWithPHP = protocol.Inventory{Nginx: "1.24.0", PHP: []string{"8.3", "8.4"}}

func testSite() protocol.Site {
	return protocol.Site{Domain: "example.com", Aliases: []string{"www.example.com"}, SystemUser: "k_example_com", WebDir: "public", PHPVersion: "8.3"}
}

func TestSiteCreate(t *testing.T) {
	f := newFakeSystem(t)
	s := testSite()
	if res := f.run(siteApplySteps(s, stackWithPHP)); !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	for _, cmd := range []string{
		"useradd --create-home --home-dir /home/k_example_com --shell /usr/sbin/nologin --user-group k_example_com",
		"usermod --append --groups k_example_com www-data",
		"/usr/sbin/php-fpm8.3 --test",
		"systemctl reload-or-restart php8.3-fpm",
		"nginx -t",
		"systemctl reload-or-restart nginx",
	} {
		if !f.ran(cmd) {
			t.Errorf("did not run %q; ran %q", cmd, f.commands)
		}
	}
	if info, err := os.Stat(f.root + "/home/k_example_com"); err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("home: %v %v", info, err)
	}
	if index := f.read("/home/k_example_com/example.com/public/index.php"); !strings.Contains(index, "<h1>example.com is ready</h1>") {
		t.Fatalf("index.php = %q", index)
	}
	pool := f.read("/etc/php/8.3/fpm/pool.d/keel-example.com.conf")
	for _, want := range []string{"[keel-example.com]", "user = k_example_com", "listen = /run/php/keel-example.com-php8.3.sock", "listen.owner = www-data"} {
		if !strings.Contains(pool, want) {
			t.Errorf("pool misses %q:\n%s", want, pool)
		}
	}
	vhost := f.read("/etc/nginx/sites-available/keel-example.com.conf")
	for _, want := range []string{
		"server_name example.com www.example.com;",
		"root /home/k_example_com/example.com/public;",
		"fastcgi_pass unix:/run/php/keel-example.com-php8.3.sock;",
	} {
		if !strings.Contains(vhost, want) {
			t.Errorf("vhost misses %q:\n%s", want, vhost)
		}
	}
	if dest, err := os.Readlink(f.root + "/etc/nginx/sites-enabled/keel-example.com.conf"); err != nil || dest != "/etc/nginx/sites-available/keel-example.com.conf" {
		t.Fatalf("link = %q, %v", dest, err)
	}
}

func TestSiteApplyAgainKeepsFiles(t *testing.T) {
	f := newFakeSystem(t)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	index := "/home/k_example_com/example.com/public/index.php"
	os.WriteFile(f.root+index, []byte("<?php echo 'my app';"), 0o640)
	f.commands = nil
	if res := f.run(siteApplySteps(s, stackWithPHP)); !res.OK {
		t.Fatalf("second apply: %+v", res)
	}
	if f.ran("useradd --create-home --home-dir /home/k_example_com --shell /usr/sbin/nologin --user-group k_example_com") {
		t.Fatal("created the user twice")
	}
	if f.read(index) != "<?php echo 'my app';" {
		t.Fatal("replaced the site's own index.php")
	}
}

func TestSiteChangePHPVersion(t *testing.T) {
	f := newFakeSystem(t)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	s.PHPVersion = "8.4"
	f.commands = nil
	if res := f.run(siteApplySteps(s, stackWithPHP)); !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	if f.exists("/etc/php/8.3/fpm/pool.d/keel-example.com.conf") || !f.exists("/etc/php/8.4/fpm/pool.d/keel-example.com.conf") {
		t.Fatal("pool did not move to PHP 8.4")
	}
	// The new pool is up and Nginx points at it before the old pool goes.
	order := []string{
		"systemctl reload-or-restart php8.4-fpm",
		"wait /run/php/keel-example.com-php8.4.sock",
		"systemctl reload-or-restart nginx",
		"systemctl reload-or-restart php8.3-fpm",
	}
	last := -1
	for _, cmd := range order {
		i := slices.Index(f.commands, cmd)
		if i <= last {
			t.Fatalf("%q not in order %q: %q", cmd, order, f.commands)
		}
		last = i
	}
	if !strings.Contains(f.read("/etc/nginx/sites-available/keel-example.com.conf"), "keel-example.com-php8.4.sock") {
		t.Fatal("nginx still points at the old socket")
	}
}

// If PHP-FPM won't come back with the new pool, the old config returns and
// the service is restarted, so the version's other sites keep running.
func TestSitePoolReloadFails(t *testing.T) {
	f := newFakeSystem(t)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	before := f.read("/etc/php/8.3/fpm/pool.d/keel-example.com.conf")
	f.failReload = "php8.3-fpm"
	f.commands = nil
	s.SystemUser = "k_other"
	f.users["k_other"] = &user.User{Username: "k_other", HomeDir: "/home/k_other", Uid: strconv.Itoa(os.Getuid()), Gid: strconv.Itoa(os.Getgid())}
	os.MkdirAll(f.root+"/home/k_other", 0o755)
	res := f.run(siteApplySteps(s, stackWithPHP))
	if res.OK || !strings.Contains(res.Error, "previous pool config was restored") {
		t.Fatalf("res = %+v", res)
	}
	if f.read("/etc/php/8.3/fpm/pool.d/keel-example.com.conf") != before {
		t.Fatal("the previous pool config was not restored")
	}
	if !f.ran("systemctl restart php8.3-fpm") {
		t.Fatalf("did not restart PHP-FPM: %q", f.commands)
	}
	if slices.Contains(f.commands, "nginx -t") {
		t.Fatal("went on to Nginx after PHP-FPM failed")
	}
}

func TestSiteNginxRejectsNewSite(t *testing.T) {
	f := newFakeSystem(t)
	f.failNginx = true
	res := f.run(siteApplySteps(testSite(), stackWithPHP))
	if res.OK || !strings.Contains(res.Error, "previous config was kept") {
		t.Fatalf("res = %+v", res)
	}
	if f.exists("/etc/nginx/sites-available/keel-example.com.conf") || f.exists("/etc/nginx/sites-enabled/keel-example.com.conf") {
		t.Fatal("left the rejected config in place")
	}
	if f.ran("systemctl reload-or-restart nginx") {
		t.Fatal("reloaded nginx with a rejected config")
	}
}

func TestSiteNginxRejectsUpdate(t *testing.T) {
	f := newFakeSystem(t)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	before := f.read("/etc/nginx/sites-available/keel-example.com.conf")
	s.Aliases = []string{"shop.example.com"}
	f.failNginx = true
	if res := f.run(siteApplySteps(s, stackWithPHP)); res.OK {
		t.Fatal("update succeeded although nginx -t failed")
	}
	if f.read("/etc/nginx/sites-available/keel-example.com.conf") != before || !f.exists("/etc/nginx/sites-enabled/keel-example.com.conf") {
		t.Fatal("the working config was not kept")
	}
}

func TestSiteFPMRejectsPool(t *testing.T) {
	f := newFakeSystem(t)
	f.failFPM = true
	res := f.run(siteApplySteps(testSite(), stackWithPHP))
	if res.OK || f.exists("/etc/php/8.3/fpm/pool.d/keel-example.com.conf") || f.exists("/etc/nginx/sites-available/keel-example.com.conf") {
		t.Fatalf("res = %+v; pool or vhost left behind", res)
	}
}

func TestSiteNeedsStack(t *testing.T) {
	f := newFakeSystem(t)
	for _, inv := range []protocol.Inventory{{PHP: []string{"8.3"}}, {Nginx: "1.24.0", PHP: []string{"8.4"}}} {
		if res := f.run(siteApplySteps(testSite(), inv)); res.OK || !strings.Contains(res.Error, "Stack tab") {
			t.Fatalf("%+v: res = %+v", inv, res)
		}
	}
	if len(f.commands) != 0 {
		t.Fatalf("ran commands: %q", f.commands)
	}
}

func TestSiteRefusesForeignUser(t *testing.T) {
	f := newFakeSystem(t)
	f.users["k_example_com"] = &user.User{Username: "k_example_com", HomeDir: "/var/lib/something"}
	if res := f.run(siteApplySteps(testSite(), stackWithPHP)); res.OK || !strings.Contains(res.Error, "already exists with home") {
		t.Fatalf("res = %+v", res)
	}
	if res := f.run(siteDeleteSteps(protocol.Site{Domain: "example.com", SystemUser: "k_example_com", PHPVersion: "8.3", DeleteFiles: true})); res.OK {
		t.Fatal("deleted a user whose home is elsewhere")
	}
	for _, c := range f.commands {
		if strings.HasPrefix(c, "userdel") {
			t.Fatalf("ran %q", c)
		}
	}
}

func TestSiteDelete(t *testing.T) {
	f := newFakeSystem(t)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	f.commands = nil
	if res := f.run(siteDeleteSteps(s)); !res.OK {
		t.Fatalf("delete: %+v", res)
	}
	for _, p := range []string{"/etc/nginx/sites-available/keel-example.com.conf", "/etc/nginx/sites-enabled/keel-example.com.conf", "/etc/php/8.3/fpm/pool.d/keel-example.com.conf"} {
		if f.exists(p) {
			t.Errorf("%s still exists", p)
		}
	}
	if !f.exists("/home/k_example_com/example.com/public/index.php") {
		t.Fatal("deleted the files without being asked to")
	}
	if !f.ran("systemctl reload-or-restart nginx") || !f.ran("systemctl reload-or-restart php8.3-fpm") {
		t.Fatalf("commands = %q", f.commands)
	}

	s.DeleteFiles = true
	f.commands = nil
	if res := f.run(siteDeleteSteps(s)); !res.OK || !f.ran("userdel --remove k_example_com") {
		t.Fatalf("delete with files: %+v, %q", res, f.commands)
	}
	if !f.ran("gpasswd --delete www-data k_example_com") || f.groups["k_example_com"] {
		t.Fatalf("group left behind: %q", f.commands)
	}
}

func TestSiteDeleteNginxBrokenElsewhere(t *testing.T) {
	f := newFakeSystem(t)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	f.failNginx = true
	if res := f.run(siteDeleteSteps(s)); res.OK {
		t.Fatal("delete succeeded although nginx -t failed")
	}
	if !f.exists("/etc/nginx/sites-available/keel-example.com.conf") || !f.exists("/etc/nginx/sites-enabled/keel-example.com.conf") {
		t.Fatal("the site's config was not put back")
	}
}

func TestRunSiteJobRejectsBadParams(t *testing.T) {
	f := newFakeSystem(t)
	var logs logCapture
	for _, raw := range []string{`nope`, `{"domain":"example.com","system_user":"root","php_version":"8.3"}`, `{"domain":"../etc","system_user":"k_x","php_version":"8.3"}`} {
		res := runQueuedJob(context.Background(), logs.send, "j1", protocol.Job{Action: protocol.ActionSiteDelete, Params: []byte(raw)})
		if res.OK {
			t.Fatalf("%s accepted", raw)
		}
	}
	if len(f.commands) != 0 {
		t.Fatalf("ran %q", f.commands)
	}
}

var stackWithCertbot = protocol.Inventory{Nginx: "1.24.0", PHP: []string{"8.3"}, Certbot: "2.9.0"}

func sslSite() protocol.Site {
	s := testSite()
	s.SSL = true
	s.ACME = &protocol.ACME{Email: "ops@example.com", Server: "https://acme.test/dir"}
	return s
}

func TestHTTPSiteServesACMEChallenges(t *testing.T) {
	f := newFakeSystem(t)
	f.run(siteApplySteps(testSite(), stackWithPHP))
	vhost := f.read("/etc/nginx/sites-available/keel-example.com.conf")
	if !strings.Contains(vhost, "location ^~ /.well-known/acme-challenge/ {\n        root /var/lib/keel/acme;") || strings.Contains(vhost, "443") {
		t.Fatalf("vhost:\n%s", vhost)
	}
}

func TestSiteIssueCertificate(t *testing.T) {
	f := newFakeSystem(t)
	if res := f.run(siteApplySteps(sslSite(), stackWithCertbot)); !res.OK {
		t.Fatalf("apply: %+v", res)
	}
	want := "certbot certonly --webroot --webroot-path /var/lib/keel/acme --cert-name keel-example.com --email ops@example.com " +
		"--agree-tos --no-eff-email --non-interactive --keep-until-expiring --domain example.com --domain www.example.com --server https://acme.test/dir"
	if !f.ran(want) {
		t.Fatalf("certbot not run as %q; ran %q", want, f.commands)
	}
	vhost := f.read("/etc/nginx/sites-available/keel-example.com.conf")
	for _, part := range []string{
		"return 301 https://$host$request_uri;",
		"listen 443 ssl http2;",
		"ssl_certificate /etc/letsencrypt/live/keel-example.com/fullchain.pem;",
		"ssl_certificate_key /etc/letsencrypt/live/keel-example.com/privkey.pem;",
		"root /home/k_example_com/example.com/public;",
	} {
		if !strings.Contains(vhost, part) {
			t.Errorf("vhost misses %q:\n%s", part, vhost)
		}
	}
	// The challenge location stays on port 80, ahead of the redirect.
	if strings.Index(vhost, "acme-challenge") > strings.Index(vhost, "return 301") || strings.Index(vhost, "root /home") < strings.Index(vhost, "listen 443") {
		t.Fatalf("blocks out of order:\n%s", vhost)
	}
	if hook := f.read(renewHookPath); !strings.Contains(hook, "systemctl reload nginx") {
		t.Fatalf("renew hook = %q", hook)
	}
}

func TestSiteCertbotFails(t *testing.T) {
	f := newFakeSystem(t)
	f.failCertbot = true
	res := f.run(siteApplySteps(sslSite(), stackWithCertbot))
	if res.OK || !strings.Contains(res.Error, "points at this server") {
		t.Fatalf("res = %+v", res)
	}
	// The site still works over HTTP.
	if vhost := f.read("/etc/nginx/sites-available/keel-example.com.conf"); strings.Contains(vhost, "443") || !strings.Contains(vhost, "root /home/k_example_com") {
		t.Fatalf("vhost:\n%s", vhost)
	}
}

func TestSiteSSLNeedsCertbot(t *testing.T) {
	f := newFakeSystem(t)
	if res := f.run(siteApplySteps(sslSite(), stackWithPHP)); res.OK || !strings.Contains(res.Error, "Certbot is not installed") {
		t.Fatalf("res = %+v", res)
	}
}

func TestSiteDeleteRemovesCertificate(t *testing.T) {
	f := newFakeSystem(t)
	s := sslSite()
	f.run(siteApplySteps(s, stackWithCertbot))
	if res := f.run(siteDeleteSteps(s)); !res.OK || !f.ran("certbot delete --cert-name keel-example.com --non-interactive") {
		t.Fatalf("res = %+v, ran %q", res, f.commands)
	}
}

func TestReadCertificates(t *testing.T) {
	dir := t.TempDir()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notAfter := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"example.com", "www.example.com"}, NotBefore: time.Now(), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(dir+"/keel-example.com", 0o755)
	os.WriteFile(dir+"/keel-example.com/cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.MkdirAll(dir+"/other.org", 0o755) // not Keel's
	os.WriteFile(dir+"/other.org/cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.MkdirAll(dir+"/keel-broken.com", 0o755)
	os.WriteFile(dir+"/keel-broken.com/cert.pem", []byte("garbage"), 0o644)

	certs := readCertificates(dir)
	if len(certs) != 1 || certs[0].Domain != "example.com" || !certs[0].NotAfter.Equal(notAfter) || !slices.Equal(certs[0].Names, tmpl.DNSNames) {
		t.Fatalf("certs = %+v", certs)
	}
}
