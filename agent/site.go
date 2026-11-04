package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// Hooks the site code uses to touch the system, so tests can point it at a
// temporary directory and fake commands.
var (
	sysRoot    = "" // prefix for every path
	runCommand = func(ctx context.Context, out io.Writer, name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = commandEnv()
		cmd.Stdout, cmd.Stderr = out, out
		cmd.WaitDelay = 5 * time.Second
		return cmd.Run()
	}
	lookupUser  = user.Lookup
	lookupGroup = user.LookupGroup
	// waitForSocket waits until PHP-FPM accepts connections on a pool's
	// socket: systemctl reload returns before PHP-FPM has opened it.
	waitForSocket = func(ctx context.Context, path string) error {
		deadline := time.Now().Add(15 * time.Second)
		for {
			conn, err := net.DialTimeout("unix", rooted(path), time.Second)
			if err == nil {
				conn.Close()
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("PHP-FPM did not start listening on %s", path)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
)

func rooted(path string) string { return sysRoot + path }

// Config files Keel owns for a site. Every name starts with keel- so Keel never
// touches files it did not write.
func poolPath(phpVersion, domain string) string {
	return "/etc/php/" + phpVersion + "/fpm/pool.d/keel-" + domain + ".conf"
}
func vhostPath(domain string) string { return "/etc/nginx/sites-available/keel-" + domain + ".conf" }
func vhostLink(domain string) string { return "/etc/nginx/sites-enabled/keel-" + domain + ".conf" }

// fpmSocket includes the PHP version, so while a site moves to another version
// the old and new pools never fight over one socket.
func fpmSocket(domain, version string) string {
	return "/run/php/keel-" + domain + "-php" + version + ".sock"
}
func fpmService(version string) string { return "php" + version + "-fpm" }

var poolTemplate = template.Must(template.New("pool").Parse(`; Managed by Keel for {{.Domain}}. Keel rewrites this file; change the site in the panel instead.
[keel-{{.Domain}}]
user = {{.SystemUser}}
group = {{.SystemUser}}
listen = {{.Socket}}
listen.owner = www-data
listen.group = www-data
listen.mode = 0660
pm = ondemand
pm.max_children = 10
pm.process_idle_timeout = 30s
pm.max_requests = 500
chdir = /
`))

// acmeWebroot holds ACME HTTP challenges for every site, so issuing a
// certificate doesn't depend on the site's own files or web directory.
const acmeWebroot = "/var/lib/keel/acme"

func certDir(s protocol.Site) string { return "/etc/letsencrypt/live/" + s.CertName() }

var vhostTemplate = template.Must(template.New("vhost").Parse(`# Managed by Keel for {{.Domain}}. Keel rewrites this file; change the site in the panel instead.
server {
    listen 80;
    listen [::]:80;
    server_name {{.ServerNames}};

    location ^~ /.well-known/acme-challenge/ {
        root ` + acmeWebroot + `;
        default_type text/plain;
    }
{{- if .TLS}}

    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name {{.ServerNames}};

    ssl_certificate {{.CertDir}}/fullchain.pem;
    ssl_certificate_key {{.CertDir}}/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:keel_ssl:10m;
    ssl_session_timeout 1d;
{{- end}}

    root {{.Root}};
    index index.php index.html;
    charset utf-8;
    client_max_body_size 64m;

    access_log /var/log/nginx/keel-{{.Domain}}.access.log;
    error_log /var/log/nginx/keel-{{.Domain}}.error.log;

    location / {
        try_files $uri $uri/ /index.php?$query_string;
    }

    location = /favicon.ico { access_log off; log_not_found off; }
    location = /robots.txt  { access_log off; log_not_found off; }

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_pass unix:{{.Socket}};
        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;
        include fastcgi_params;
        fastcgi_hide_header X-Powered-By;
    }

    location ~ /\.(?!well-known).* {
        deny all;
    }
}
`))

var indexTemplate = template.Must(template.New("index").Parse(`<?php
// Placeholder from Keel. Deploy your site to replace it.
?><!doctype html>
<meta charset="utf-8">
<title>{{.Domain}}</title>
<h1>{{.Domain}} is ready</h1>
<p>Served by PHP <?= PHP_VERSION ?> as <?= htmlspecialchars(posix_getpwuid(posix_geteuid())['name']) ?>.</p>
`))

type siteTemplateData struct {
	protocol.Site
	ServerNames string
	Root        string
	Socket      string
	TLS         bool // serve HTTPS with the certificate in CertDir
	CertDir     string
}

func templateData(s protocol.Site, tls bool) siteTemplateData {
	return siteTemplateData{Site: s, ServerNames: strings.Join(s.Names(), " "), Root: s.Root(),
		Socket: fpmSocket(s.Domain, s.PHPVersion), TLS: tls, CertDir: certDir(s)}
}

func render(t *template.Template, s protocol.Site) []byte { return renderTLS(t, s, false) }

func renderTLS(t *template.Template, s protocol.Site, tls bool) []byte {
	var b bytes.Buffer
	if err := t.Execute(&b, templateData(s, tls)); err != nil {
		panic(err) // the templates are fixed and the data validated
	}
	return b.Bytes()
}

// siteApplySteps creates a site or brings it in line with s. Running it again
// with the same s changes nothing.
func siteApplySteps(s protocol.Site, inv protocol.Inventory) []step {
	steps := []step{
		{title: "check the web stack", fn: func(context.Context, io.Writer) error { return checkSiteStack(s, inv) }},
		{title: "set up system user " + s.SystemUser, fn: func(ctx context.Context, out io.Writer) error { return ensureSiteUser(ctx, out, s) }},
		{title: "set up " + s.Dir(), fn: func(ctx context.Context, out io.Writer) error { return ensureSiteDirs(out, s) }},
		{title: "configure PHP " + s.PHPVersion + "-FPM", fn: func(ctx context.Context, out io.Writer) error { return applyPool(ctx, out, s) }},
		{title: "configure Nginx", fn: func(ctx context.Context, out io.Writer) error { return applyVhost(ctx, out, s) }},
		// Only once Nginx uses the new pool, so a version change has no gap.
		{title: "remove pools for other PHP versions", fn: func(ctx context.Context, out io.Writer) error { return removePools(ctx, out, s.Domain, s.PHPVersion) }},
	}
	if s.SSL {
		steps = append(steps,
			step{title: "get the certificate for " + strings.Join(s.Names(), ", "), fn: func(ctx context.Context, out io.Writer) error { return ensureCert(ctx, out, s, inv) }},
			step{title: "serve the site over HTTPS", fn: func(ctx context.Context, out io.Writer) error { return applyVhost(ctx, out, s) }},
		)
	}
	return steps
}

func siteDeleteSteps(s protocol.Site) []step {
	steps := []step{
		{title: "remove the Nginx config", fn: func(ctx context.Context, out io.Writer) error { return removeVhost(ctx, out, s) }},
		{title: "remove the PHP-FPM pool", fn: func(ctx context.Context, out io.Writer) error { return removePools(ctx, out, s.Domain, "") }},
	}
	steps = append(steps, step{title: "delete the certificate", fn: func(ctx context.Context, out io.Writer) error { return deleteCert(ctx, out, s) }})
	if s.DeleteFiles {
		steps = append(steps, step{title: "delete system user " + s.SystemUser + " and " + s.HomeDir(), fn: func(ctx context.Context, out io.Writer) error {
			return deleteSiteUser(ctx, out, s)
		}})
	}
	return steps
}

func checkSiteStack(s protocol.Site, inv protocol.Inventory) error {
	if inv.Nginx == "" {
		return errors.New("Nginx is not installed; install it from the server's Stack tab")
	}
	if !slices.Contains(inv.PHP, s.PHPVersion) {
		return fmt.Errorf("PHP %s is not installed; install it from the server's Stack tab", s.PHPVersion)
	}
	return nil
}

func ensureSiteUser(ctx context.Context, out io.Writer, s protocol.Site) error {
	u, err := lookupUser(s.SystemUser)
	var unknown user.UnknownUserError
	switch {
	case errors.As(err, &unknown):
		if err := runCommand(ctx, out, "useradd", "--create-home", "--home-dir", s.HomeDir(),
			"--shell", "/usr/sbin/nologin", "--user-group", s.SystemUser); err != nil {
			return fmt.Errorf("useradd: %w", err)
		}
		fmt.Fprintf(out, "created user %s\n", s.SystemUser)
	case err != nil:
		return err
	case u.HomeDir != s.HomeDir():
		return fmt.Errorf("user %s already exists with home %s, not %s", s.SystemUser, u.HomeDir, s.HomeDir())
	default:
		fmt.Fprintf(out, "user %s already exists\n", s.SystemUser)
	}
	// Other site users can't look inside; Nginx can, through the group.
	if err := os.Chmod(rooted(s.HomeDir()), 0o750); err != nil {
		return err
	}
	if err := runCommand(ctx, out, "usermod", "--append", "--groups", s.SystemUser, "www-data"); err != nil {
		return fmt.Errorf("adding www-data to group %s: %w", s.SystemUser, err)
	}
	return nil
}

func siteOwner(s protocol.Site) (uid, gid int, err error) {
	u, err := lookupUser(s.SystemUser)
	if err != nil {
		return 0, 0, err
	}
	uid, _ = strconv.Atoi(u.Uid)
	gid, _ = strconv.Atoi(u.Gid)
	return uid, gid, nil
}

// ensureSiteDirs makes the site and web directories, owned by the site user,
// and puts a placeholder page in an empty web directory.
func ensureSiteDirs(out io.Writer, s protocol.Site) error {
	uid, gid, err := siteOwner(s)
	if err != nil {
		return err
	}
	dir := s.HomeDir()
	for _, part := range strings.Split(strings.TrimPrefix(s.Root(), s.HomeDir()+"/"), "/") {
		dir += "/" + part
		switch err := os.Mkdir(rooted(dir), 0o750); {
		case err == nil:
			if err := os.Lchown(rooted(dir), uid, gid); err != nil {
				return err
			}
			fmt.Fprintf(out, "created %s\n", dir)
		case errors.Is(err, fs.ErrExist):
			if info, err := os.Lstat(rooted(dir)); err != nil || !info.IsDir() {
				return fmt.Errorf("%s exists and is not a directory", dir)
			}
		default:
			return err
		}
	}
	entries, err := os.ReadDir(rooted(s.Root()))
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		index := rooted(s.Root() + "/index.php")
		if err := os.WriteFile(index, render(indexTemplate, s), 0o640); err != nil {
			return err
		}
		if err := os.Lchown(index, uid, gid); err != nil {
			return err
		}
		fmt.Fprintf(out, "added a placeholder %s/index.php\n", s.Root())
	}
	return nil
}

// replaceFile writes content to path and returns a function that puts back
// what was there before, or removes the file if there was nothing.
func replaceFile(path string, content []byte) (restore func() error, err error) {
	old, err := os.ReadFile(rooted(path))
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := writeFileAtomic(rooted(path), content); err != nil {
		return nil, err
	}
	return func() error {
		if existed {
			return writeFileAtomic(rooted(path), old)
		}
		return removeIfExists(rooted(path))
	}, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func reload(ctx context.Context, out io.Writer, service string) error {
	if err := runCommand(ctx, out, "systemctl", "reload-or-restart", service); err != nil {
		return fmt.Errorf("reloading %s: %w", service, err)
	}
	return nil
}

// applyPool writes the site's pool for its PHP version and reloads PHP-FPM.
// If the config test fails, or PHP-FPM doesn't come back with the pool, the
// previous config is restored so the version's other sites keep running.
func applyPool(ctx context.Context, out io.Writer, s protocol.Site) error {
	path := poolPath(s.PHPVersion, s.Domain)
	restore, err := replaceFile(path, render(poolTemplate, s))
	if err != nil {
		return err
	}
	if err := runCommand(ctx, out, "/usr/sbin/php-fpm"+s.PHPVersion, "--test"); err != nil {
		if rerr := restore(); rerr != nil {
			return fmt.Errorf("the PHP-FPM config test failed, and restoring %s failed too: %v", path, rerr)
		}
		return errors.New("the PHP-FPM config test failed; the previous pool config was kept")
	}
	fmt.Fprintf(out, "wrote %s\n", path)
	err = reload(ctx, out, fpmService(s.PHPVersion))
	if err == nil {
		err = waitForSocket(ctx, fpmSocket(s.Domain, s.PHPVersion))
	}
	if err != nil {
		fmt.Fprintf(out, "%v\nputting back the previous pool config\n", err)
		rerr := restore()
		if rerr == nil {
			rerr = runCommand(ctx, out, "systemctl", "restart", fpmService(s.PHPVersion))
		}
		if rerr != nil {
			return fmt.Errorf("%v; restoring the previous config failed too: %v", err, rerr)
		}
		return fmt.Errorf("%v; the previous pool config was restored", err)
	}
	return nil
}

// removePools removes the site's pool from every PHP version except keep.
func removePools(ctx context.Context, out io.Writer, domain, keep string) error {
	matches, err := filepath.Glob(rooted("/etc/php/*/fpm/pool.d/keel-" + domain + ".conf"))
	if err != nil {
		return err
	}
	for _, m := range matches {
		version := strings.Split(strings.TrimPrefix(m, rooted("/etc/php/")), "/")[0]
		if version == keep {
			continue
		}
		if err := os.Remove(m); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed %s\n", poolPath(version, domain))
		if err := reload(ctx, out, fpmService(version)); err != nil {
			return err
		}
	}
	return nil
}

// applyVhost writes and enables the site's Nginx config. If nginx -t rejects
// it, the previous config comes back and Nginx keeps running unchanged. An SSL
// site is served over HTTPS once its certificate exists, and over HTTP until
// then.
func applyVhost(ctx context.Context, out io.Writer, s protocol.Site) error {
	tls := false
	if s.SSL {
		_, err := os.Stat(rooted(certDir(s) + "/fullchain.pem"))
		tls = err == nil
		if !tls {
			fmt.Fprintln(out, "no certificate yet, so the site is served over HTTP for now")
		}
	}
	restoreFile, err := replaceFile(vhostPath(s.Domain), renderTLS(vhostTemplate, s, tls))
	if err != nil {
		return err
	}
	linked, err := ensureLink(vhostPath(s.Domain), vhostLink(s.Domain))
	if err != nil {
		restoreFile()
		return err
	}
	if err := runCommand(ctx, out, "nginx", "-t"); err != nil {
		rerr := restoreFile()
		if linked {
			rerr = errors.Join(rerr, removeIfExists(rooted(vhostLink(s.Domain))))
		}
		if rerr != nil {
			return fmt.Errorf("nginx -t failed, and putting the old config back failed too: %v", rerr)
		}
		return errors.New("nginx -t rejected the new config; the previous config was kept")
	}
	fmt.Fprintf(out, "wrote %s\n", vhostPath(s.Domain))
	return reload(ctx, out, "nginx")
}

// ensureLink makes link a symlink to target, reporting whether it created it.
func ensureLink(target, link string) (bool, error) {
	if dest, err := os.Readlink(rooted(link)); err == nil {
		if dest == target {
			return false, nil
		}
		return false, fmt.Errorf("%s already exists and points to %s", link, dest)
	}
	if err := os.MkdirAll(filepath.Dir(rooted(link)), 0o755); err != nil {
		return false, err
	}
	return true, os.Symlink(target, rooted(link))
}

func removeVhost(ctx context.Context, out io.Writer, s protocol.Site) error {
	old, err := os.ReadFile(rooted(vhostPath(s.Domain)))
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(out, "no Nginx config to remove")
		return removeIfExists(rooted(vhostLink(s.Domain)))
	} else if err != nil {
		return err
	}
	if err := removeIfExists(rooted(vhostLink(s.Domain))); err != nil {
		return err
	}
	if err := os.Remove(rooted(vhostPath(s.Domain))); err != nil {
		return err
	}
	if err := runCommand(ctx, out, "nginx", "-t"); err != nil {
		// Something else in the Nginx config is broken; leave the site as it was.
		rerr := errors.Join(writeFileAtomic(rooted(vhostPath(s.Domain)), old), os.Symlink(vhostPath(s.Domain), rooted(vhostLink(s.Domain))))
		if rerr != nil {
			return fmt.Errorf("nginx -t failed, and putting the site's config back failed too: %v", rerr)
		}
		return errors.New("nginx -t failed after removing the site's config, so it was put back; fix the Nginx config and try again")
	}
	fmt.Fprintf(out, "removed %s\n", vhostPath(s.Domain))
	return reload(ctx, out, "nginx")
}

func deleteSiteUser(ctx context.Context, out io.Writer, s protocol.Site) error {
	u, err := lookupUser(s.SystemUser)
	var unknown user.UnknownUserError
	if errors.As(err, &unknown) {
		fmt.Fprintf(out, "user %s does not exist\n", s.SystemUser)
		return nil
	} else if err != nil {
		return err
	}
	if u.HomeDir != s.HomeDir() {
		return fmt.Errorf("user %s has home %s, not %s; not deleting it", s.SystemUser, u.HomeDir, s.HomeDir())
	}
	// While www-data is in the site's group, userdel keeps the group.
	if err := runCommand(ctx, out, "gpasswd", "--delete", "www-data", s.SystemUser); err != nil {
		fmt.Fprintf(out, "(www-data was not in group %s)\n", s.SystemUser)
	}
	// PHP-FPM workers of the removed pool may take a moment to exit.
	var lastErr error
	for range 10 {
		if lastErr = runCommand(ctx, out, "userdel", "--remove", s.SystemUser); lastErr == nil {
			fmt.Fprintf(out, "deleted user %s and %s\n", s.SystemUser, s.HomeDir())
			if _, err := lookupGroup(s.SystemUser); err == nil {
				if err := runCommand(ctx, out, "groupdel", s.SystemUser); err != nil {
					return fmt.Errorf("groupdel: %w", err)
				}
			}
			return nil
		}
		var exit *exec.ExitError
		if !errors.As(lastErr, &exit) || exit.ExitCode() != 8 { // 8: user still has processes
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("userdel: %w", lastErr)
}

// renewHook makes certbot reload Nginx after it renews any certificate.
const renewHookPath = "/etc/letsencrypt/renewal-hooks/deploy/keel-reload-nginx"
const renewHook = "#!/bin/sh\n# Installed by Keel: load renewed certificates.\nsystemctl reload nginx\n"

// ensureCert asks certbot for a certificate covering the site's names. With
// --keep-until-expiring it does nothing when the certificate already covers
// them and isn't due for renewal, so it is cheap to run on every update.
func ensureCert(ctx context.Context, out io.Writer, s protocol.Site, inv protocol.Inventory) error {
	if inv.Certbot == "" {
		return errors.New("Certbot is not installed; install it from the server's Stack tab")
	}
	if err := os.MkdirAll(rooted(acmeWebroot), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(rooted(filepath.Dir(renewHookPath)), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(rooted(renewHookPath), []byte(renewHook), 0o755); err != nil {
		return err
	}
	args := []string{"certonly", "--webroot", "--webroot-path", acmeWebroot, "--cert-name", s.CertName(),
		"--email", s.ACME.Email, "--agree-tos", "--no-eff-email", "--non-interactive", "--keep-until-expiring"}
	for _, n := range s.Names() {
		args = append(args, "--domain", n)
	}
	if s.ACME.Server != "" {
		args = append(args, "--server", s.ACME.Server)
	}
	if err := runCommand(ctx, out, "certbot", args...); err != nil {
		return fmt.Errorf("certbot could not get a certificate (check that %s points at this server and port 80 is open): %w",
			strings.Join(s.Names(), ", "), err)
	}
	return nil
}

func deleteCert(ctx context.Context, out io.Writer, s protocol.Site) error {
	if _, err := os.Stat(rooted("/etc/letsencrypt/renewal/" + s.CertName() + ".conf")); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(out, "no certificate to delete")
		return nil
	}
	if err := runCommand(ctx, out, "certbot", "delete", "--cert-name", s.CertName(), "--non-interactive"); err != nil {
		return fmt.Errorf("certbot delete: %w", err)
	}
	return nil
}

// readCertificates lists the certificates of Keel sites in certbot's live
// directory.
func readCertificates(liveDir string) []protocol.Certificate {
	certs := []protocol.Certificate{}
	dirs, _ := filepath.Glob(filepath.Join(liveDir, "keel-*"))
	for _, dir := range dirs {
		b, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
		if err != nil {
			continue
		}
		block, _ := pem.Decode(b)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		certs = append(certs, protocol.Certificate{
			Domain:   strings.TrimPrefix(filepath.Base(dir), "keel-"),
			Names:    cert.DNSNames,
			NotAfter: cert.NotAfter,
		})
	}
	return certs
}
