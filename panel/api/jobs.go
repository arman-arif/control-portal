package api

import (
	"net/http"
	"strconv"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

// maxLogResponse caps how much job output one GET /api/jobs/{id} returns; the
// web app asks again with ?after= for the rest.
const maxLogResponse = 256 << 10

func (a *API) enqueue(w http.ResponseWriter, r *http.Request, serverID int64, action string, params any) {
	job, err := a.jobs.Enqueue(r.Context(), userFrom(r).ID, serverID, action, params)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (a *API) handleRefreshPackages(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	if !sv.Linked {
		writeError(w, http.StatusConflict, "this server is not linked yet")
		return
	}
	a.enqueue(w, r, sv.ID, protocol.ActionPackagesRefresh, nil)
}

func (a *API) handleListJobs(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	jobs, err := a.store.ListJobs(r.Context(), userFrom(r).ID, sv.ID, 50)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (a *API) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	job, err := a.store.GetJob(r.Context(), userFrom(r).ID, id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	logs, err := a.store.JobLogs(r.Context(), job.ID, after, maxLogResponse)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		store.Job
		Logs []store.JobLogLine `json:"logs"`
	}{job, logs})
}
