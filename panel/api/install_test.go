package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// renderInstallScript returns the script a panel with this fingerprint serves.
func renderInstallScript(t *testing.T, fingerprint string) string {
	t.Helper()
	a := &API{cfg: Config{PublicURL: "https://panel.test", TLSFingerprint: fingerprint}}
	rec := httptest.NewRecorder()
	a.handleInstallScript(rec, httptest.NewRequest(http.MethodGet, "/install.sh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

// runRegisterLine runs the script's register line with a stand-in for the agent
// and returns the arguments the agent would get.
func runRegisterLine(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "keel-agent")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var vars, register string
	for _, line := range strings.Split(script, "\n") {
		switch {
		case strings.HasPrefix(line, "PANEL_URL=") || strings.HasPrefix(line, "TLS_FINGERPRINT="):
			vars += line + "\n"
		case strings.HasPrefix(line, "/usr/local/bin/keel-agent register"):
			register = strings.Replace(line, "/usr/local/bin/keel-agent", fake, 1)
		}
	}
	if register == "" {
		t.Fatal("no register line in the script")
	}
	out, err := exec.Command("bash", "-c", "set -euo pipefail\n"+vars+"TOKEN=tok\n"+register).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func TestInstallScriptPassesFingerprint(t *testing.T) {
	fp := "spki-sha256:" + strings.Repeat("ab", 32)
	got := runRegisterLine(t, renderInstallScript(t, fp))
	want := "register --panel https://panel.test --token tok --fingerprint " + fp
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestInstallScriptWithoutFingerprint(t *testing.T) {
	got := runRegisterLine(t, renderInstallScript(t, ""))
	want := "register --panel https://panel.test --token tok"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestInstallScriptParses(t *testing.T) {
	for _, fp := range []string{"", "spki-sha256:" + strings.Repeat("ab", 32)} {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(renderInstallScript(t, fp))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fingerprint %q: %v: %s", fp, err, out)
		}
	}
}
