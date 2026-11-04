// Command keel-panel runs the Keel control panel: the web app, its JSON API,
// and the endpoint agents connect to.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/arman-arif/control-portal/panel/api"
	"github.com/arman-arif/control-portal/panel/hub"
	"github.com/arman-arif/control-portal/panel/secrets"
	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const usage = `usage: keel-panel [command]

commands:
  serve                  run the panel (the default)
  init [--env-file PATH] create the secret key file (default ` + defaultEnvFile + `)
  fingerprint [--cert] URL
                         print the ` + tlsFingerprintVar + ` value for an https URL
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("keel-panel: ")

	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	switch {
	case command == "init":
		runInit(args)
	case command == "fingerprint":
		runFingerprint(args)
	case command == "serve" && len(args) == 0:
		serve()
	case command == "help" || command == "-h" || command == "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// checkSecretKey makes sure the panel has a usable key before it starts, so a
// bad setup shows at startup rather than when a secret is first needed.
func checkSecretKey() error {
	help := "run `keel-panel init` to create a key, then load the file it writes"
	key := os.Getenv(secretKeyVar)
	if key == "" {
		return fmt.Errorf("%s is required: %s", secretKeyVar, help)
	}
	if _, err := secrets.New(key); err != nil {
		return fmt.Errorf("%s is not usable (%v): %s", secretKeyVar, err, help)
	}
	return nil
}

func serve() {
	if err := checkSecretKey(); err != nil {
		log.Fatal(err)
	}
	dbURL := os.Getenv("KEEL_DATABASE_URL")
	if dbURL == "" {
		log.Fatal("KEEL_DATABASE_URL is required, e.g. postgres://keel:secret@localhost:5432/keel")
	}
	listen := env("KEEL_LISTEN", ":8080")
	publicURL, err := checkPublicURL(env("KEEL_PUBLIC_URL", "http://localhost:8080"))
	if err != nil {
		log.Fatalf("KEEL_PUBLIC_URL: %v", err)
	}
	tlsFingerprint, warning, err := checkTLSFingerprint(os.Getenv(tlsFingerprintVar), publicURL)
	if err != nil {
		log.Fatalf("%s: %v", tlsFingerprintVar, err)
	}
	if warning != "" {
		log.Print(warning)
	}
	acmeServer := os.Getenv("KEEL_ACME_SERVER")
	if acmeServer != "" {
		if err := (protocol.ACME{Email: "check@example.com", Server: acmeServer}).Validate(); err != nil {
			log.Fatalf("KEEL_ACME_SERVER: %v", err)
		}
		log.Printf("site certificates come from %s instead of Let's Encrypt", acmeServer)
	}
	cfg := api.Config{
		PublicURL:      publicURL,
		WebDir:         env("KEEL_WEB_DIR", "web/dist"),
		AgentDir:       env("KEEL_AGENT_DIR", "dist"),
		AllowSignup:    os.Getenv("KEEL_ALLOW_SIGNUP") == "true",
		TrustProxy:     os.Getenv("KEEL_TRUST_PROXY") == "true",
		TLSFingerprint: tlsFingerprint,
		ACMEServer:     acmeServer,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrating database: %v", err)
	}

	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := st.DeleteExpiredSessions(ctx); err != nil {
					log.Printf("cleaning sessions: %v", err)
				}
			}
		}
	}()

	a := api.New(cfg, st, hub.New())
	if err := a.Recover(ctx); err != nil {
		log.Fatalf("cleaning up jobs: %v", err)
	}
	srv := &http.Server{
		Addr:              listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()
	log.Printf("listening on %s, public URL %s", listen, publicURL)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// checkPublicURL makes sure the URL is a plain http(s) base URL, since it is
// embedded in the shell install script.
func checkPublicURL(raw string) (string, error) {
	raw = strings.TrimRight(raw, "/")
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("must look like https://panel.example.com, got %q", raw)
	}
	if strings.ContainsAny(raw, "'\"`$\\ \t\n;&|<>()") {
		return "", fmt.Errorf("contains characters that are not allowed: %q", raw)
	}
	return raw, nil
}
