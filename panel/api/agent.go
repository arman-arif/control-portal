package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
	"github.com/coder/websocket"
)

// agentIdleTimeout drops an agent that has sent nothing for this long. Agents
// send stats every 5 seconds.
const agentIdleTimeout = 60 * time.Second

// handleAgentRegister swaps a one-time install token for a long-lived agent
// secret.
func (a *API) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Hostname string `json:"hostname"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	key := "agent:" + a.clientIP(r)
	if !a.logins.Allow(key) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts; try again later")
		return
	}
	secret := newToken()
	id, err := a.store.LinkAgent(r.Context(), hashToken(in.Token), hashToken(secret), truncate(in.Hostname, 255), a.clientIP(r))
	if isNotFound(err) {
		a.logins.Fail(key)
		writeError(w, http.StatusUnauthorized, "install token is invalid, expired or already used; get a new command from the panel")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_id": id, "secret": secret})
}

func (a *API) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" {
		writeError(w, http.StatusUnauthorized, "missing agent credentials")
		return
	}
	serverID, err := a.store.ServerIDByAgentSecret(r.Context(), hashToken(secret))
	if isNotFound(err) {
		writeError(w, http.StatusUnauthorized, "unknown agent")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	agent := a.hub.Attach(serverID, conn)
	defer a.hub.Detach(agent)
	defer conn.CloseNow()
	ip := a.clientIP(r)
	log.Printf("agent for server %d connected from %s", serverID, ip)

	// The request context ends when the handler returns, so use a detached
	// one for database writes made while the socket is open.
	ctx := context.WithoutCancel(r.Context())
	var lastTouch time.Time
	for {
		readCtx, cancel := context.WithTimeout(ctx, agentIdleTimeout)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			log.Printf("agent for server %d disconnected: %v", serverID, err)
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		switch env.Type {
		case protocol.TypeHello:
			var h protocol.Hello
			if json.Unmarshal(env.Data, &h) != nil {
				continue
			}
			err := a.store.UpdateAgentInfo(ctx, serverID, store.AgentInfo{
				Hostname: truncate(h.Hostname, 255), OS: truncate(h.OS, 255), Kernel: truncate(h.Kernel, 255),
				Arch: truncate(h.Arch, 32), AgentVersion: truncate(h.AgentVersion, 64), IP: ip, CPUs: h.CPUs,
			})
			if err != nil {
				log.Printf("saving agent info for server %d: %v", serverID, err)
			}
			lastTouch = time.Now()
		case protocol.TypeStats:
			var s protocol.Stats
			if json.Unmarshal(env.Data, &s) != nil {
				continue
			}
			a.hub.RecordStats(serverID, s)
			if time.Since(lastTouch) > 30*time.Second {
				if err := a.store.TouchServer(ctx, serverID); err != nil {
					log.Printf("updating last seen for server %d: %v", serverID, err)
				}
				lastTouch = time.Now()
			}
		case protocol.TypeServices:
			var svcs []protocol.Service
			if json.Unmarshal(env.Data, &svcs) != nil {
				continue
			}
			managed := svcs[:0]
			for _, s := range svcs {
				if protocol.IsManagedService(s.Name) {
					managed = append(managed, s)
				}
			}
			a.hub.RecordServices(serverID, managed)
		case protocol.TypeJobResult:
			var res protocol.JobResult
			if json.Unmarshal(env.Data, &res) != nil {
				continue
			}
			agent.HandleJobResult(env.ID, res)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
