package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

var errPinMismatch = errors.New("panel certificate does not match this agent's pinned fingerprint")

// panelClient returns the HTTP client for talking to the panel. With a
// fingerprint, every TLS connection must present a matching certificate, on top
// of the usual check against the system's trusted CAs.
func panelClient(fingerprint string) (*http.Client, error) {
	if fingerprint == "" {
		return http.DefaultClient, nil
	}
	want, err := protocol.ParseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		// Unlike VerifyPeerCertificate, this also runs on resumed sessions.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errPinMismatch
			}
			leaf := cs.PeerCertificates[0]
			if !want.Matches(leaf) {
				return fmt.Errorf("%w: pinned %s, panel presented %s", errPinMismatch, want, want.Of(leaf))
			}
			return nil
		},
	}
	return &http.Client{Transport: transport}, nil
}

// cmdRepin changes the pinned fingerprint after the panel's certificate has
// changed. It refuses a fingerprint the panel isn't presenting, since saving a
// wrong one would keep the agent offline.
func cmdRepin(args []string) {
	fs := flag.NewFlagSet("repin", flag.ExitOnError)
	fingerprint := fs.String("fingerprint", "", "new fingerprint, as printed by keel-panel fingerprint")
	cfgPath := fs.String("config", defaultConfigPath, "agent config path")
	fs.Parse(args)
	if *fingerprint == "" || fs.NArg() > 0 {
		usage()
	}
	parsed, err := protocol.ParseFingerprint(*fingerprint)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := readConfig(*cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	cfg.TLSFingerprint = parsed.String()

	if err := checkPin(cfg); errors.Is(err, errPinMismatch) {
		log.Fatalf("not saved: %v", err)
	} else if err != nil {
		log.Printf("warning: could not check the fingerprint against the panel: %v", err)
	}
	if !strings.HasPrefix(cfg.PanelURL, "https://") {
		log.Printf("warning: %s is not https, so the fingerprint has no effect", cfg.PanelURL)
	}
	if err := writeConfig(*cfgPath, cfg); err != nil {
		log.Fatalf("writing config: %v", err)
	}
	log.Printf("pinned %s in %s; apply it with: systemctl restart keel-agent", cfg.TLSFingerprint, *cfgPath)
}

// checkPin connects to the panel using the config's fingerprint.
func checkPin(cfg config) error {
	client, err := panelClient(cfg.TLSFingerprint)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, cfg.PanelURL+"/", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// checkConfigFingerprint stops a damaged config from quietly turning pinning off.
func checkConfigFingerprint(cfg config) error {
	if cfg.TLSFingerprint == "" {
		return nil
	}
	if _, err := protocol.ParseFingerprint(cfg.TLSFingerprint); err != nil {
		return fmt.Errorf("tls_fingerprint: %w", err)
	}
	return nil
}
