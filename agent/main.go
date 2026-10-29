// Command keel-agent runs on each managed server. It links the server to a
// Keel panel, reports health stats, and carries out jobs the panel sends.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/arman-arif/control-portal/protocol"
	"github.com/coder/websocket"
)

var version = "dev"

const defaultConfigPath = "/etc/keel/agent.json"

type config struct {
	PanelURL string `json:"panel_url"`
	ServerID int64  `json:"server_id"`
	Secret   string `json:"secret"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("keel-agent: ")
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "register":
		cmdRegister(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: keel-agent register --panel URL --token TOKEN [--config PATH]")
	fmt.Fprintln(os.Stderr, "       keel-agent run [--config PATH]")
	fmt.Fprintln(os.Stderr, "       keel-agent version")
	os.Exit(2)
}

func cmdRegister(args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	panel := fs.String("panel", "", "panel URL, e.g. https://panel.example.com")
	token := fs.String("token", "", "one-time install token from the panel")
	cfgPath := fs.String("config", defaultConfigPath, "where to write the agent config")
	fs.Parse(args)
	if *panel == "" || *token == "" {
		usage()
	}
	panelURL := strings.TrimRight(*panel, "/")
	hostname, _ := os.Hostname()

	body, _ := json.Marshal(map[string]string{"token": *token, "hostname": hostname})
	resp, err := http.Post(panelURL+"/api/agent/register", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("contacting panel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Fatalf("panel refused registration (%s): %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		ServerID int64  `json:"server_id"`
		Secret   string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		log.Fatalf("reading panel response: %v", err)
	}
	cfg := config{PanelURL: panelURL, ServerID: out.ServerID, Secret: out.Secret}
	if err := writeConfig(*cfgPath, cfg); err != nil {
		log.Fatalf("writing config: %v", err)
	}
	log.Printf("registered as server %d, config saved to %s", out.ServerID, *cfgPath)
}

func writeConfig(path string, cfg config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readConfig(path string) (config, error) {
	var cfg config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if cfg.PanelURL == "" || cfg.Secret == "" {
		return cfg, errors.New("config is missing panel_url or secret; run keel-agent register first")
	}
	return cfg, nil
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath, "agent config path")
	fs.Parse(args)
	cfg, err := readConfig(*cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	backoff := time.Second
	for {
		started := time.Now()
		err := runSession(ctx, cfg)
		if ctx.Err() != nil {
			return
		}
		log.Printf("disconnected: %v", err)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func wsURL(panelURL string) string {
	switch {
	case strings.HasPrefix(panelURL, "https://"):
		return "wss://" + strings.TrimPrefix(panelURL, "https://") + "/api/agent/ws"
	case strings.HasPrefix(panelURL, "http://"):
		return "ws://" + strings.TrimPrefix(panelURL, "http://") + "/api/agent/ws"
	}
	return panelURL + "/api/agent/ws"
}

// runSession holds one WebSocket connection to the panel until it drops.
func runSession(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	conn, resp, err := websocket.Dial(dialCtx, wsURL(cfg.PanelURL), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Secret}},
	})
	dialCancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("panel rejected this agent's credentials; was the server removed from the panel?")
		}
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	log.Printf("connected to %s", cfg.PanelURL)

	send := func(typ, id string, v any) error {
		b, err := protocol.Encode(typ, id, v)
		if err != nil {
			return err
		}
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return conn.Write(wctx, websocket.MessageText, b)
	}
	sendServices := func() {
		svcs, err := listServices(ctx)
		if err != nil {
			log.Printf("listing services: %v", err)
			return
		}
		if err := send(protocol.TypeServices, "", svcs); err != nil {
			log.Printf("sending services: %v", err)
		}
	}

	if err := send(protocol.TypeHello, "", hello()); err != nil {
		return err
	}
	sendServices()

	go func() {
		collector := &statsCollector{}
		collector.Collect() // prime the CPU sample
		statsTick := time.NewTicker(5 * time.Second)
		svcTick := time.NewTicker(30 * time.Second)
		defer statsTick.Stop()
		defer svcTick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-statsTick.C:
				s, err := collector.Collect()
				if err != nil {
					log.Printf("collecting stats: %v", err)
					continue
				}
				if err := send(protocol.TypeStats, "", s); err != nil {
					cancel()
					return
				}
			case <-svcTick.C:
				sendServices()
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			log.Printf("bad message from panel: %v", err)
			continue
		}
		if env.Type != protocol.TypeJob {
			continue
		}
		var job protocol.Job
		if err := json.Unmarshal(env.Data, &job); err != nil {
			send(protocol.TypeJobResult, env.ID, protocol.JobResult{Error: "malformed job"})
			continue
		}
		go func(id string, job protocol.Job) {
			log.Printf("job %s: %s %s", id, job.Action, job.Service)
			var res protocol.JobResult
			if protocol.IsQueuedAction(job.Action) {
				res = runQueuedJob(ctx, send, id, job)
			} else {
				res = runServiceJob(ctx, job)
			}
			if err := send(protocol.TypeJobResult, id, res); err != nil {
				log.Printf("sending job result: %v", err)
			}
			sendServices()
		}(env.ID, job)
	}
}

func hello() protocol.Hello {
	h := protocol.Hello{Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), AgentVersion: version}
	h.Hostname, _ = os.Hostname()
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		h.OS = osReleaseName(string(b))
	}
	var u syscall.Utsname
	if syscall.Uname(&u) == nil {
		h.Kernel = utsString(u.Release[:])
	}
	return h
}

func osReleaseName(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func utsString[T int8 | uint8](b []T) string {
	var sb strings.Builder
	for _, c := range b {
		if c == 0 {
			break
		}
		sb.WriteByte(byte(c))
	}
	return sb.String()
}
