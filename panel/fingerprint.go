package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

const tlsFingerprintVar = "KEEL_TLS_FINGERPRINT"

// runFingerprint is the `keel-panel fingerprint` command. It prints the value
// to set for KEEL_TLS_FINGERPRINT or pass to `keel-agent repin`.
func runFingerprint(args []string) {
	flags := flag.NewFlagSet("fingerprint", flag.ExitOnError)
	wholeCert := flags.Bool("cert", false, "hash the whole certificate, which changes at every renewal, instead of its public key")
	flags.Parse(args)
	if flags.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: keel-panel fingerprint [--cert] https://panel.example.com")
		os.Exit(2)
	}
	leaf, err := fetchLeafCert(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "keel-panel fingerprint: %v\n", err)
		os.Exit(1)
	}
	fp := protocol.SPKIFingerprint(leaf)
	if *wholeCert {
		fp = protocol.CertFingerprint(leaf)
	}
	fmt.Printf("%s=%s\n", tlsFingerprintVar, fp)
}

// fetchLeafCert returns the certificate served at an https URL, after the usual
// check against the system's trusted CAs.
func fetchLeafCert(rawURL string) (*x509.Certificate, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" || u.Hostname() == "" {
		return nil, fmt.Errorf("need an https:// URL, got %q", rawURL)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := (&tls.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return nil, errors.New("the server sent no certificate")
	}
	return certs[0], nil
}

// checkTLSFingerprint validates KEEL_TLS_FINGERPRINT and returns the value to
// give agents, which is empty unless agents reach the panel over https. A
// non-empty warning should be logged.
func checkTLSFingerprint(raw, publicURL string) (fingerprint, warning string, err error) {
	https := strings.HasPrefix(publicURL, "https://")
	if raw == "" {
		if https {
			return "", "agents will not pin the panel's certificate: " + tlsFingerprintVar + " is not set; see `keel-panel fingerprint`", nil
		}
		return "", "", nil
	}
	fp, err := protocol.ParseFingerprint(raw)
	if err != nil {
		return "", "", err
	}
	if !https {
		return "", tlsFingerprintVar + " is ignored because KEEL_PUBLIC_URL is not https", nil
	}
	return fp.String(), "", nil
}
