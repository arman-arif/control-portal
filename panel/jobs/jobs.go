// Package jobs runs queued jobs on servers: it stores them, sends one at a
// time to each server's agent, records the output the agent streams back,
// and moves on to the next job when one finishes.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arman-arif/control-portal/panel/hub"
	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

// MaxLogBytes caps the stored output of one job.
const MaxLogBytes = 4 << 20

type running struct {
	jobID     int64
	agent     *hub.Agent // connection the job was sent on
	logBytes  int
	truncated bool
}

type Runner struct {
	store *store.Store
	hub   *hub.Hub

	// OnFinish, if set before the runner is used, is called with each job
	// once its outcome is stored.
	OnFinish func(ctx context.Context, job store.Job)
	// Prepare, if set, returns the params to send for a job, for adding
	// secrets that must not be stored with the job. An error fails the job.
	Prepare func(ctx context.Context, job store.Job) (json.RawMessage, error)

	// mu serializes dispatch so each server has at most one running job, and
	// guards the running map, which is the source of truth for which
	// connection owns that job.
	mu      sync.Mutex
	running map[int64]*running // by server ID
}

func New(st *store.Store, h *hub.Hub) *Runner {
	return &Runner{store: st, hub: h, running: map[int64]*running{}}
}

// Enqueue stores a job and starts it if the server is idle and online.
func (r *Runner) Enqueue(ctx context.Context, userID, serverID int64, ref store.JobRef, action string, params any) (store.Job, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return store.Job{}, err
		}
		raw = b
	}
	job, err := r.store.CreateJob(ctx, userID, serverID, ref, action, raw)
	if err != nil {
		return job, err
	}
	r.mu.Lock()
	r.dispatchLocked(ctx, serverID)
	r.mu.Unlock()
	return job, nil
}

// Connected starts waiting work for a server whose agent just connected.
func (r *Runner) Connected(ctx context.Context, serverID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dispatchLocked(ctx, serverID)
}

// Disconnected fails the job running on a connection that has closed, then
// hands waiting work to a newer connection if there is one.
func (r *Runner) Disconnected(ctx context.Context, a *hub.Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run := r.running[a.ServerID]; run != nil && run.agent == a {
		r.finishLocked(ctx, a.ServerID, run, false, "the agent disconnected while the job was running", nil)
	}
	r.dispatchLocked(ctx, a.ServerID)
}

// HandleLog stores output for a job if it is the one running on a.
func (r *Runner) HandleLog(ctx context.Context, a *hub.Agent, envID string, text string) {
	id, ok := parseID(envID)
	if !ok || text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.running[a.ServerID]
	if run == nil || run.agent != a || run.jobID != id || run.truncated {
		return
	}
	r.appendLocked(ctx, run, text)
}

// HandleResult finishes a job if it is the one running on a, and reports
// whether envID belonged to a queued job.
func (r *Runner) HandleResult(ctx context.Context, a *hub.Agent, envID string, res protocol.JobResult) bool {
	id, ok := parseID(envID)
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.running[a.ServerID]
	if run == nil || run.agent != a || run.jobID != id {
		return true
	}
	if res.Output != "" && !run.truncated {
		r.appendLocked(ctx, run, res.Output)
	}
	result := res.Result
	if len(result) > protocol.MaxJobResult {
		result = nil
	}
	r.finishLocked(ctx, a.ServerID, run, res.OK, res.Error, result)
	r.dispatchLocked(ctx, a.ServerID)
	return true
}

func (r *Runner) appendLocked(ctx context.Context, run *running, text string) {
	if run.logBytes+len(text) > MaxLogBytes {
		text = "\n[keel: output truncated, the log limit was reached]\n"
		run.truncated = true
	}
	run.logBytes += len(text)
	if err := r.store.AppendJobLog(ctx, run.jobID, text); err != nil {
		log.Printf("saving log of job %d: %v", run.jobID, err)
	}
}

func (r *Runner) finishLocked(ctx context.Context, serverID int64, run *running, ok bool, errMsg string, result json.RawMessage) {
	delete(r.running, serverID)
	if ok {
		errMsg = ""
	} else if errMsg == "" {
		errMsg = "the job failed"
	}
	job, err := r.store.FinishJob(ctx, run.jobID, ok, errMsg, result)
	if err != nil {
		log.Printf("finishing job %d: %v", run.jobID, err)
		return
	}
	if r.OnFinish != nil {
		r.OnFinish(ctx, job)
	}
}

// FailInterrupted fails the jobs that were running when the panel stopped,
// since no agent can still be working on them. Call it once at startup.
func (r *Runner) FailInterrupted(ctx context.Context) error {
	jobs, err := r.store.FailRunningJobs(ctx, "the panel restarted while the job was running")
	if err != nil {
		return err
	}
	if r.OnFinish != nil {
		for _, job := range jobs {
			r.OnFinish(ctx, job)
		}
	}
	return nil
}

// dispatchLocked sends the next queued job of an idle, online server.
func (r *Runner) dispatchLocked(ctx context.Context, serverID int64) {
	if r.running[serverID] != nil {
		return
	}
	a := r.hub.Agent(serverID)
	if a == nil {
		return
	}
	job, err := r.store.ClaimNextJob(ctx, serverID)
	if errors.Is(err, store.ErrNotFound) {
		return
	} else if err != nil {
		log.Printf("claiming next job for server %d: %v", serverID, err)
		return
	}
	params := job.Params
	if r.Prepare != nil {
		if params, err = r.Prepare(ctx, job); err != nil {
			log.Printf("preparing job %d: %v", job.ID, err)
			r.finishLocked(ctx, serverID, &running{jobID: job.ID, agent: a}, false, "could not prepare the job: "+err.Error(), nil)
			r.dispatchLocked(ctx, serverID)
			return
		}
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	msg := protocol.Job{Action: job.Action, Params: params}
	if err := a.Send(sendCtx, protocol.TypeJob, protocol.QueuedJobPrefix+strconv.FormatInt(job.ID, 10), msg); err != nil {
		// The connection is going away; Connected will retry on the next one.
		if err := r.store.RequeueJob(ctx, job.ID); err != nil {
			log.Printf("requeueing job %d: %v", job.ID, err)
		}
		return
	}
	r.running[serverID] = &running{jobID: job.ID, agent: a}
}

func parseID(envID string) (int64, bool) {
	s, ok := strings.CutPrefix(envID, protocol.QueuedJobPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}
