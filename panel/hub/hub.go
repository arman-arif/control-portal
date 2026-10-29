// Package hub tracks the agents currently connected to the panel, the latest
// data they reported, and jobs waiting for a reply.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/arman-arif/control-portal/protocol"
	"github.com/coder/websocket"
)

// HistorySize is how many stats samples are kept per server (10 minutes at
// one sample every 5 seconds).
const HistorySize = 120

var ErrOffline = errors.New("server is offline")

// Agent is one live agent connection.
type Agent struct {
	ServerID int64
	conn     *websocket.Conn

	mu      sync.Mutex
	pending map[string]chan protocol.JobResult
}

// Send writes one message to the agent.
func (a *Agent) Send(ctx context.Context, typ, id string, v any) error {
	b, err := protocol.Encode(typ, id, v)
	if err != nil {
		return err
	}
	return a.conn.Write(ctx, websocket.MessageText, b)
}

type serverState struct {
	history  []protocol.Stats
	services []protocol.Service
}

type Hub struct {
	mu     sync.RWMutex
	agents map[int64]*Agent
	state  map[int64]*serverState
}

func New() *Hub {
	return &Hub{agents: map[int64]*Agent{}, state: map[int64]*serverState{}}
}

// Attach registers a new connection for a server, closing any previous one.
func (h *Hub) Attach(serverID int64, conn *websocket.Conn) *Agent {
	a := &Agent{ServerID: serverID, conn: conn, pending: map[string]chan protocol.JobResult{}}
	h.mu.Lock()
	old := h.agents[serverID]
	h.agents[serverID] = a
	h.mu.Unlock()
	if old != nil {
		go old.conn.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
	return a
}

// Detach removes the agent if it is still the current connection for its
// server, and fails any jobs still waiting on it.
func (h *Hub) Detach(a *Agent) {
	h.mu.Lock()
	if h.agents[a.ServerID] == a {
		delete(h.agents, a.ServerID)
	}
	h.mu.Unlock()
	a.mu.Lock()
	for id, ch := range a.pending {
		ch <- protocol.JobResult{Error: "agent disconnected"}
		delete(a.pending, id)
	}
	a.mu.Unlock()
}

// Remove disconnects a server's agent and forgets its data, for example when
// the server is deleted from the panel.
func (h *Hub) Remove(serverID int64) {
	h.mu.Lock()
	a := h.agents[serverID]
	delete(h.agents, serverID)
	delete(h.state, serverID)
	h.mu.Unlock()
	if a != nil {
		go a.conn.Close(websocket.StatusNormalClosure, "server removed from panel")
	}
}

// Agent returns the current connection for a server, or nil when offline.
func (h *Hub) Agent(serverID int64) *Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.agents[serverID]
}

func (h *Hub) Online(serverID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.agents[serverID] != nil
}

func (h *Hub) stateLocked(serverID int64) *serverState {
	st := h.state[serverID]
	if st == nil {
		st = &serverState{}
		h.state[serverID] = st
	}
	return st
}

func (h *Hub) RecordStats(serverID int64, s protocol.Stats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.stateLocked(serverID)
	st.history = append(st.history, s)
	if n := len(st.history); n > HistorySize {
		st.history = slices.Clone(st.history[n-HistorySize:])
	}
}

func (h *Hub) RecordServices(serverID int64, svcs []protocol.Service) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stateLocked(serverID).services = svcs
}

type Snapshot struct {
	Online   bool               `json:"online"`
	History  []protocol.Stats   `json:"history"`
	Services []protocol.Service `json:"services"`
}

func (h *Hub) Snapshot(serverID int64) Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	snap := Snapshot{Online: h.agents[serverID] != nil, History: []protocol.Stats{}, Services: []protocol.Service{}}
	if st := h.state[serverID]; st != nil {
		snap.History = slices.Clone(st.history)
		if st.services != nil {
			snap.Services = slices.Clone(st.services)
		}
	}
	return snap
}

// HandleJobResult delivers a job_result message to whoever is waiting on it,
// reporting whether anyone was.
func (a *Agent) HandleJobResult(id string, res protocol.JobResult) bool {
	a.mu.Lock()
	ch := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()
	if ch != nil {
		ch <- res
	}
	return ch != nil
}

// RunJob sends a job to a server's agent and waits for the result.
func (h *Hub) RunJob(ctx context.Context, serverID int64, job protocol.Job) (protocol.JobResult, error) {
	h.mu.RLock()
	a := h.agents[serverID]
	h.mu.RUnlock()
	if a == nil {
		return protocol.JobResult{}, ErrOffline
	}

	idb := make([]byte, 8)
	rand.Read(idb)
	id := hex.EncodeToString(idb)
	ch := make(chan protocol.JobResult, 1)
	a.mu.Lock()
	a.pending[id] = ch
	a.mu.Unlock()
	cleanup := func() {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := a.Send(ctx, protocol.TypeJob, id, job); err != nil {
		cleanup()
		return protocol.JobResult{}, ErrOffline
	}
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		cleanup()
		return protocol.JobResult{}, ctx.Err()
	}
}
