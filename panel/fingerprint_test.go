package main

import (
	"strings"
	"testing"
)

func TestCheckTLSFingerprint(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	good := "spki-sha256:" + digest

	fp, warning, err := checkTLSFingerprint("spki-sha256:"+strings.ToUpper(digest), "https://panel.example.com")
	if err != nil || warning != "" || fp != good {
		t.Fatalf("https with fingerprint: got %q, %q, %v", fp, warning, err)
	}

	fp, warning, err = checkTLSFingerprint("", "https://panel.example.com")
	if err != nil || fp != "" || !strings.Contains(warning, "will not pin") {
		t.Fatalf("https without fingerprint: got %q, %q, %v", fp, warning, err)
	}

	fp, warning, err = checkTLSFingerprint("", "http://10.0.3.1:8080")
	if err != nil || fp != "" || warning != "" {
		t.Fatalf("http without fingerprint: got %q, %q, %v", fp, warning, err)
	}

	fp, warning, err = checkTLSFingerprint(good, "http://10.0.3.1:8080")
	if err != nil || fp != "" || !strings.Contains(warning, "ignored") {
		t.Fatalf("http with fingerprint: got %q, %q, %v", fp, warning, err)
	}

	if _, _, err := checkTLSFingerprint("oops", "https://panel.example.com"); err == nil {
		t.Fatal("accepted a bad fingerprint")
	}
	if _, _, err := checkTLSFingerprint("oops", "http://10.0.3.1:8080"); err == nil {
		t.Fatal("accepted a bad fingerprint on http")
	}
}

func TestFetchLeafCertNeedsHTTPS(t *testing.T) {
	for _, in := range []string{"http://panel.example.com", "panel.example.com", "https://"} {
		if _, err := fetchLeafCert(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
