package protocol

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strings"
	"testing"
)

func TestParseFingerprintForms(t *testing.T) {
	digest := strings.Repeat("ab", sha256.Size)
	want := FingerprintSPKI + ":" + digest
	colons := strings.TrimSuffix(strings.Repeat("AB:", sha256.Size), ":")
	for _, in := range []string{want, FingerprintSPKI + ":" + strings.ToUpper(digest), FingerprintSPKI + ":" + colons} {
		f, err := ParseFingerprint(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if f.String() != want {
			t.Fatalf("%q: got %q, want %q", in, f, want)
		}
	}
}

func TestParseFingerprintRejects(t *testing.T) {
	digest := strings.Repeat("ab", sha256.Size)
	for _, in := range []string{
		"",
		digest,
		"sha256:" + digest,
		FingerprintSPKI,
		FingerprintSPKI + ":",
		FingerprintSPKI + ":" + digest[:62],
		FingerprintSPKI + ":" + digest + "ab",
		FingerprintSPKI + ":" + digest[:62] + "zz",
		FingerprintCert + ":" + digest + "'; reboot; '",
	} {
		if _, err := ParseFingerprint(in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestFingerprintMatches(t *testing.T) {
	first := &x509.Certificate{Raw: []byte("certificate one"), RawSubjectPublicKeyInfo: []byte("key A")}
	renewed := &x509.Certificate{Raw: []byte("certificate two"), RawSubjectPublicKeyInfo: []byte("key A")}
	rekeyed := &x509.Certificate{Raw: []byte("certificate three"), RawSubjectPublicKeyInfo: []byte("key B")}

	spki := SPKIFingerprint(first)
	if !spki.Matches(first) || !spki.Matches(renewed) {
		t.Error("public key fingerprint should match any certificate with that key")
	}
	if spki.Matches(rekeyed) {
		t.Error("public key fingerprint matched a different key")
	}

	cert := CertFingerprint(first)
	if !cert.Matches(first) {
		t.Error("certificate fingerprint should match its certificate")
	}
	if cert.Matches(renewed) {
		t.Error("certificate fingerprint matched a different certificate")
	}

	if (Fingerprint{}).Matches(first) {
		t.Error("the zero value matched a certificate")
	}
}

// Stored pins depend on exactly what gets hashed.
func TestFingerprintValue(t *testing.T) {
	sum := sha256.Sum256([]byte("key A"))
	cert := &x509.Certificate{RawSubjectPublicKeyInfo: []byte("key A")}
	want := FingerprintSPKI + ":" + hex.EncodeToString(sum[:])
	if got := SPKIFingerprint(cert).String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	parsed, err := ParseFingerprint(want)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Matches(cert) {
		t.Fatal("parsed fingerprint does not match")
	}
}
