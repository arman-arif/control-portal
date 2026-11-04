package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/arman-arif/control-portal/panel/hub"
	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

// The .env file lives only on the server: these handlers ask the agent
// directly and keep nothing of the content.

func (a *API) runEnvJob(w http.ResponseWriter, r *http.Request, site store.Site, action, content string) (protocol.JobResult, bool) {
	if site.Status == store.SiteDeleting {
		writeError(w, http.StatusConflict, "this site is being deleted")
		return protocol.JobResult{}, false
	}
	res, err := a.hub.RunJob(r.Context(), site.ServerID, protocol.Job{Action: action, Params: mustJSON(protocol.EnvParams{Site: a.siteParams(site), Content: content})})
	if errors.Is(err, hub.ErrOffline) {
		writeError(w, http.StatusConflict, "the server is offline, and the .env file is only on the server")
		return res, false
	} else if err != nil {
		writeError(w, http.StatusGatewayTimeout, "the server did not answer in time")
		return res, false
	}
	return res, true
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func (a *API) handleReadEnv(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	res, ok := a.runEnvJob(w, r, site, protocol.ActionSiteEnvRead, "")
	if !ok {
		return
	}
	if !res.OK {
		writeError(w, http.StatusUnprocessableEntity, res.Error)
		return
	}
	var file protocol.EnvFile
	if err := json.Unmarshal(res.Result, &file); err != nil {
		internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, file)
}

func (a *API) handleWriteEnv(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	content := strings.ReplaceAll(in.Content, "\r\n", "\n")
	if len(content) > protocol.MaxEnvFile || !utf8.ValidString(content) {
		writeError(w, http.StatusBadRequest, "the .env file must be UTF-8 text of at most 256 KB")
		return
	}
	res, ok := a.runEnvJob(w, r, site, protocol.ActionSiteEnvWrite, content)
	if !ok {
		return
	}
	// The history shows that .env changed, never what it holds.
	if err := a.store.RecordJob(r.Context(), userFrom(r).ID, site.ServerID, store.SiteRef(site.ID), protocol.ActionSiteEnvWrite,
		json.RawMessage(`{"redacted":true,"domain":`+string(mustJSON(site.Domain))+`}`), res.OK, res.Error); err != nil {
		log.Printf("recording .env change of site %d: %v", site.ID, err)
	}
	if !res.OK {
		writeError(w, http.StatusUnprocessableEntity, res.Error)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
