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
	"github.com/arman-arif/control-portal/panel/store"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("keel-panel: ")

	dbURL := os.Getenv("KEEL_DATABASE_URL")
	if dbURL == "" {
		log.Fatal("KEEL_DATABASE_URL is required, e.g. postgres://keel:secret@localhost:5432/keel")
	}
	listen := env("KEEL_LISTEN", ":8080")
	publicURL, err := checkPublicURL(env("KEEL_PUBLIC_URL", "http://localhost:8080"))
	if err != nil {
		log.Fatalf("KEEL_PUBLIC_URL: %v", err)
	}
	cfg := api.Config{
		PublicURL:   publicURL,
		WebDir:      env("KEEL_WEB_DIR", "web/dist"),
		AgentDir:    env("KEEL_AGENT_DIR", "dist"),
		AllowSignup: os.Getenv("KEEL_ALLOW_SIGNUP") == "true",
		TrustProxy:  os.Getenv("KEEL_TRUST_PROXY") == "true",
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
	// No agent can still be working on a job sent before this start.
	if err := st.FailRunningJobs(ctx, "the panel restarted while the job was running"); err != nil {
		log.Fatalf("cleaning up jobs: %v", err)
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

	srv := &http.Server{
		Addr:              listen,
		Handler:           api.New(cfg, st, hub.New()).Handler(),
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
