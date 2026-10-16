package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arman-arif/control-portal/panel/hub"
	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

const installTokenTTL = time.Hour

type serverView struct {
	store.Server
	Online bool            `json:"online"`
	Latest *protocol.Stats `json:"latest"`
}

func (a *API) view(sv store.Server) serverView {
	snap := a.hub.Snapshot(sv.ID)
	v := serverView{Server: sv, Online: snap.Online}
	if n := len(snap.History); n > 0 && snap.Online {
		v.Latest = &snap.History[n-1]
	}
	return v
}

type installInfo struct {
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *API) installCommand(token string) string {
	return "curl -fsSL " + a.cfg.PublicURL + "/install.sh | sudo bash -s -- " + token
}

// serverFromPath loads the server named by {id} for the current user, writing
// an error response if it cannot.
func (a *API) serverFromPath(w http.ResponseWriter, r *http.Request) (store.Server, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return store.Server{}, false
	}
	sv, err := a.store.GetServer(r.Context(), userFrom(r).ID, id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "server not found")
		return sv, false
	} else if err != nil {
		internalError(w, err)
		return sv, false
	}
	return sv, true
}

func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.store.ListServers(r.Context(), userFrom(r).ID)
	if err != nil {
		internalError(w, err)
		return
	}
	out := make([]serverView, len(servers))
	for i, sv := range servers {
		out[i] = a.view(sv)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		writeError(w, http.StatusBadRequest, "name must be 1 to 64 characters")
		return
	}
	tok := newToken()
	expires := time.Now().Add(installTokenTTL)
	sv, err := a.store.CreateServer(r.Context(), userFrom(r).ID, name, hashToken(tok), expires)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"server":  a.view(sv),
		"install": installInfo{Command: a.installCommand(tok), ExpiresAt: expires},
	})
}

func (a *API) handleGetServer(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.view(sv))
}

func (a *API) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteServer(r.Context(), userFrom(r).ID, sv.ID); err != nil && !isNotFound(err) {
		internalError(w, err)
		return
	}
	a.hub.Remove(sv.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleInstallCommand issues a fresh install token for a server that has not
// been linked yet, invalidating the previous one.
func (a *API) handleInstallCommand(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	if sv.Linked {
		writeError(w, http.StatusConflict, "this server is already linked")
		return
	}
	tok := newToken()
	expires := time.Now().Add(installTokenTTL)
	if err := a.store.ResetInstallToken(r.Context(), userFrom(r).ID, sv.ID, hashToken(tok), expires); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusConflict, "this server is already linked")
			return
		}
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, installInfo{Command: a.installCommand(tok), ExpiresAt: expires})
}

func (a *API) handleServerStats(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, a.hub.Snapshot(sv.ID))
}

func (a *API) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	name, action := r.PathValue("name"), "service."+r.PathValue("action")
	if !protocol.IsManagedService(name) {
		writeError(w, http.StatusBadRequest, "this service is not managed by Keel")
		return
	}
	if !protocol.IsServiceAction(action) {
		writeError(w, http.StatusBadRequest, "action must be start, stop or restart")
		return
	}
	res, err := a.hub.RunJob(r.Context(), sv.ID, protocol.Job{Action: action, Service: name})
	if errors.Is(err, hub.ErrOffline) {
		writeError(w, http.StatusConflict, "server is offline")
		return
	} else if err != nil {
		writeError(w, http.StatusGatewayTimeout, "the server did not answer in time")
		return
	}
	status := http.StatusOK
	if !res.OK {
		status = http.StatusUnprocessableEntity
	}
	writeJSON(w, status, res)
}
