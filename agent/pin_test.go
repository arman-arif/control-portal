package main

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arman-arif/control-portal/protocol"
	"github.com/coder/websocket"
)

// pinServer is a TLS server that accepts WebSockets on any path.
func pinServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// trustingClient is panelClient for a server the test's CA pool trusts.
func trustingClient(t *testing.T, srv *httptest.Server, fingerprint string) *http.Client {
	t.Helper()
	client, err := panelClient(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs = roots
	return client
}

func dial(t *testing.T, srv *httptest.Server, client *http.Client) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL(srv.URL), &websocket.DialOptions{HTTPClient: client})
	if err == nil {
		conn.CloseNow()
	}
	return err
}

func otherFingerprint(kind string) string {
	return kind + ":" + strings.Repeat("00", 32)
}

func TestPinnedClientAcceptsMatchingCertificate(t *testing.T) {
	srv := pinServer(t)
	for _, fp := range []protocol.Fingerprint{
		protocol.SPKIFingerprint(srv.Certificate()),
		protocol.CertFingerprint(srv.Certificate()),
	} {
		client := trustingClient(t, srv, fp.String())
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("%s: GET: %v", fp, err)
		}
		resp.Body.Close()
		if err := dial(t, srv, client); err != nil {
			t.Fatalf("%s: WebSocket: %v", fp, err)
		}
	}
}

func TestPinnedClientRejectsOtherCertificate(t *testing.T) {
	srv := pinServer(t)
	for _, kind := range []string{protocol.FingerprintSPKI, protocol.FingerprintCert} {
		client := trustingClient(t, srv, otherFingerprint(kind))
		_, err := client.Get(srv.URL)
		if !errors.Is(err, errPinMismatch) {
			t.Fatalf("%s: GET: got error %v", kind, err)
		}
		if !strings.Contains(err.Error(), "panel presented "+kind) {
			t.Fatalf("%s: error does not show what was presented: %v", kind, err)
		}
		if err := dial(t, srv, client); !errors.Is(err, errPinMismatch) {
			t.Fatalf("%s: WebSocket: got error %v", kind, err)
		}
	}
}

// A matching pin is not enough on its own: the certificate must still be valid.
func TestPinnedClientStillChecksTrust(t *testing.T) {
	srv := pinServer(t)
	client, err := panelClient(protocol.SPKIFingerprint(srv.Certificate()).String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(srv.URL) // the test CA is not in the system's trusted CAs
	if err == nil {
		t.Fatal("connected to an untrusted certificate")
	}
	if errors.Is(err, errPinMismatch) {
		t.Fatalf("wrong failure: %v", err)
	}
}

func TestPanelClientWithoutFingerprint(t *testing.T) {
	client, err := panelClient("")
	if err != nil {
		t.Fatal(err)
	}
	if client != http.DefaultClient {
		t.Fatal("expected the default client")
	}
	if _, err := panelClient("sha256:abc"); err == nil {
		t.Fatal("accepted a bad fingerprint")
	}
}

func TestPlainHTTPPanelIgnoresFingerprint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client, err := panelClient(otherFingerprint(protocol.FingerprintSPKI))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// repin saves the fingerprint unless the error is a mismatch, so failing to
// reach the panel must not be reported as one.
func TestCheckPinUnreachablePanelIsNotAMismatch(t *testing.T) {
	cfg := config{PanelURL: "http://127.0.0.1:1", TLSFingerprint: otherFingerprint(protocol.FingerprintSPKI)}
	if err := checkPin(cfg); err == nil || errors.Is(err, errPinMismatch) {
		t.Fatalf("got %v", err)
	}
}

func TestConfigFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")

	// Configs written before pinning have no fingerprint and keep working.
	if err := os.WriteFile(path, []byte(`{"panel_url":"http://p","server_id":1,"secret":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSFingerprint != "" {
		t.Fatalf("got fingerprint %q", cfg.TLSFingerprint)
	}

	// Saving and loading keeps the fingerprint.
	cfg.TLSFingerprint = otherFingerprint(protocol.FingerprintSPKI)
	if err := writeConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != cfg {
		t.Fatalf("got %+v, want %+v", loaded, cfg)
	}

	// A damaged fingerprint must stop the agent, not turn pinning off.
	if err := os.WriteFile(path, []byte(`{"panel_url":"http://p","server_id":1,"secret":"s","tls_fingerprint":"oops"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(path); err == nil {
		t.Fatal("accepted a damaged fingerprint")
	}
}
