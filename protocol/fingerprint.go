package protocol

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"strings"
)

// Kinds of TLS fingerprint. An spki-sha256 fingerprint hashes the certificate's
// public key, so it survives a renewal that keeps the same key. A cert-sha256
// fingerprint hashes the whole certificate, so it changes at every renewal.
const (
	FingerprintSPKI = "spki-sha256"
	FingerprintCert = "cert-sha256"
)

// Fingerprint identifies the panel's TLS certificate for an agent to pin. Its
// zero value matches nothing.
type Fingerprint struct {
	kind string
	sum  [sha256.Size]byte
}

// ParseFingerprint reads "<kind>:<hex>". The hex may be upper case or separated
// by colons, as openssl prints it. String gives back the canonical form, which
// is safe to embed in a shell script.
func ParseFingerprint(s string) (Fingerprint, error) {
	kind, digest, ok := strings.Cut(s, ":")
	if !ok || (kind != FingerprintSPKI && kind != FingerprintCert) {
		return Fingerprint{}, fmt.Errorf("fingerprint must start with %s: or %s:", FingerprintSPKI, FingerprintCert)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(digest, ":", ""))
	if err != nil || len(b) != sha256.Size {
		return Fingerprint{}, fmt.Errorf("fingerprint must have a %d character SHA-256 hex value after %s:", 2*sha256.Size, kind)
	}
	return Fingerprint{kind: kind, sum: [sha256.Size]byte(b)}, nil
}

// SPKIFingerprint returns the fingerprint of cert's public key.
func SPKIFingerprint(cert *x509.Certificate) Fingerprint {
	return Fingerprint{kind: FingerprintSPKI, sum: sha256.Sum256(cert.RawSubjectPublicKeyInfo)}
}

// CertFingerprint returns the fingerprint of the whole of cert.
func CertFingerprint(cert *x509.Certificate) Fingerprint {
	return Fingerprint{kind: FingerprintCert, sum: sha256.Sum256(cert.Raw)}
}

// Of returns cert's fingerprint of the same kind as f.
func (f Fingerprint) Of(cert *x509.Certificate) Fingerprint {
	if f.kind == FingerprintCert {
		return CertFingerprint(cert)
	}
	return SPKIFingerprint(cert)
}

// Matches reports whether cert has this fingerprint.
func (f Fingerprint) Matches(cert *x509.Certificate) bool {
	return f.kind != "" && f.Of(cert) == f
}

func (f Fingerprint) String() string {
	return f.kind + ":" + hex.EncodeToString(f.sum[:])
}
