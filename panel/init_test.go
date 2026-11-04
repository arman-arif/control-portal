package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/panel/secrets"
)

func envFileKey(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	value, found := findEnvValue(content, secretKeyVar)
	if !found {
		t.Fatalf("no key in %q", content)
	}
	if _, err := secrets.New(value); err != nil {
		t.Fatalf("written key is not usable: %v", err)
	}
	return value
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestEnsureSecretKeyCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "keel", "panel.env")
	created, err := ensureSecretKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("reported no key written")
	}
	envFileKey(t, path)
	if got := mode(t, path); got != 0o600 {
		t.Fatalf("mode %04o, want 0600", got)
	}
}

func TestEnsureSecretKeyKeepsExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.env")
	if _, err := ensureSecretKey(path); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	created, err := ensureSecretKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("reported a new key on the second run")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("file changed: %q then %q", before, after)
	}
}

func TestEnsureSecretKeyRecognisesOtherForms(t *testing.T) {
	key := secrets.GenerateKey()
	for _, line := range []string{
		"export " + secretKeyVar + "=" + key,
		secretKeyVar + `="` + key + `"`,
		secretKeyVar + "='" + key + "'",
		"  " + secretKeyVar + "=" + key + "  ",
	} {
		path := filepath.Join(t.TempDir(), "panel.env")
		if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		created, err := ensureSecretKey(path)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if created {
			t.Fatalf("%q: not recognised, wrote a second key", line)
		}
	}
}

func TestEnsureSecretKeyAddsToExistingFile(t *testing.T) {
	for name, existing := range map[string]string{
		"trailing newline":    "KEEL_LISTEN=:9000\n",
		"no trailing newline": "KEEL_LISTEN=:9000",
		"commented out key":   "# " + secretKeyVar + "=old\nKEEL_LISTEN=:9000\n",
		"similar name":        "X" + secretKeyVar + "=old\nKEEL_LISTEN=:9000\n",
		"empty file":          "",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "panel.env")
			if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
				t.Fatal(err)
			}
			created, err := ensureSecretKey(path)
			if err != nil {
				t.Fatal(err)
			}
			if !created {
				t.Fatal("reported no key written")
			}
			envFileKey(t, path)
			content, _ := os.ReadFile(path)
			if !strings.HasPrefix(string(content), existing) {
				t.Fatalf("existing lines changed: %q", content)
			}
			if existing != "" && !strings.Contains(string(content), "KEEL_LISTEN=:9000\n") {
				t.Fatalf("key ran into the previous line: %q", content)
			}
			if got := mode(t, path); got != 0o600 {
				t.Fatalf("mode %04o, want 0600", got)
			}
		})
	}
}

// A bad key might still be the one the data was encrypted with, or a typo in
// it, so init must leave it for a person to sort out.
func TestEnsureSecretKeyRefusesToReplaceBadKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.env")
	existing := secretKeyVar + "=sup3rs3cret\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ensureSecretKey(path)
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error contains the key: %v", err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != existing {
		t.Fatalf("file changed: %q", content)
	}
}

func TestFindEnvValueLastAssignmentWins(t *testing.T) {
	value, found := findEnvValue([]byte("A=one\nB=x\nexport A=two\n"), "A")
	if !found || value != "two" {
		t.Fatalf("got %q, %v", value, found)
	}
	if _, found := findEnvValue([]byte("AB=one\n#A=two\n"), "A"); found {
		t.Fatal("matched a line that does not set A")
	}
}

func TestCheckSecretKey(t *testing.T) {
	t.Setenv(secretKeyVar, "")
	err := checkSecretKey()
	if err == nil || !strings.Contains(err.Error(), "keel-panel init") {
		t.Fatalf("missing key: got %v", err)
	}

	t.Setenv(secretKeyVar, "sup3rs3cret")
	err = checkSecretKey()
	if err == nil || !strings.Contains(err.Error(), "keel-panel init") {
		t.Fatalf("bad key: got %v", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error contains the key: %v", err)
	}

	t.Setenv(secretKeyVar, secrets.GenerateKey())
	if err := checkSecretKey(); err != nil {
		t.Fatalf("good key: %v", err)
	}
}
