package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/arman-arif/control-portal/protocol"
)

// queuedTimeouts bounds how long each queued action may run.
var queuedTimeouts = map[string]time.Duration{
	protocol.ActionPackagesRefresh: 10 * time.Minute,
	protocol.ActionStackInstall:    30 * time.Minute,
	protocol.ActionSiteCreate:      10 * time.Minute,
	protocol.ActionSiteUpdate:      10 * time.Minute,
	protocol.ActionSiteDelete:      10 * time.Minute,
	protocol.ActionSiteSSLIssue:    10 * time.Minute,
}

type sendFunc func(typ, id string, v any) error

// runQueuedJob carries out a queued job, streaming its output as job_log
// messages, and returns the result to send back.
func runQueuedJob(ctx context.Context, send sendFunc, id string, job protocol.Job) protocol.JobResult {
	timeout, ok := queuedTimeouts[job.Action]
	if !ok {
		return protocol.JobResult{Error: "unsupported action " + job.Action}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch job.Action {
	case protocol.ActionPackagesRefresh:
		return runStreamed(ctx, send, id, aptCommand(ctx, "update"))
	case protocol.ActionStackInstall:
		return runStackInstall(ctx, send, id, job.Params)
	case protocol.ActionSiteCreate, protocol.ActionSiteUpdate, protocol.ActionSiteDelete, protocol.ActionSiteSSLIssue:
		return runSiteJob(ctx, send, id, job)
	}
	return protocol.JobResult{Error: "unsupported action " + job.Action}
}

// runSiteJob validates a site again here, since its fields end up in paths and
// config files on this server.
func runSiteJob(ctx context.Context, send sendFunc, id string, job protocol.Job) protocol.JobResult {
	var s protocol.Site
	if err := json.Unmarshal(job.Params, &s); err != nil {
		return protocol.JobResult{Error: "malformed params"}
	}
	if err := s.Validate(); err != nil {
		return protocol.JobResult{Error: err.Error()}
	}
	if job.Action == protocol.ActionSiteDelete {
		return runSteps(ctx, send, id, siteDeleteSteps(s))
	}
	if job.Action == protocol.ActionSiteSSLIssue && !s.SSL {
		return protocol.JobResult{Error: "site.ssl.issue needs ssl set"}
	}
	inv, err := collectInventory(ctx)
	if err != nil {
		return protocol.JobResult{Error: "checking installed packages: " + err.Error()}
	}
	return runSteps(ctx, send, id, siteApplySteps(s, inv))
}

// runStackInstall checks the params again here, since the agent is what
// touches the server, and refuses an install that conflicts with what is
// installed now.
func runStackInstall(ctx context.Context, send sendFunc, id string, raw json.RawMessage) protocol.JobResult {
	var p protocol.StackInstall
	if err := json.Unmarshal(raw, &p); err != nil {
		return protocol.JobResult{Error: "malformed params"}
	}
	if err := p.Validate(); err != nil {
		return protocol.JobResult{Error: err.Error()}
	}
	inv, err := collectInventory(ctx)
	if err != nil {
		return protocol.JobResult{Error: "checking installed packages: " + err.Error()}
	}
	if conflict := protocol.StackConflict(p, inv); conflict != "" {
		return protocol.JobResult{Error: conflict}
	}
	return runSteps(ctx, send, id, stackRecipe(p))
}

func aptCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "apt-get", append(append([]string{}, aptOptions[:2]...), args...)...)
	cmd.Env = commandEnv()
	return cmd
}

// runStreamed runs cmd with its stdout and stderr sent to the panel as
// job_log messages while it runs.
func runStreamed(ctx context.Context, send sendFunc, id string, cmd *exec.Cmd) protocol.JobResult {
	w := newLogWriter(func(text string) { send(protocol.TypeJobLog, id, protocol.JobLog{Text: text}) })
	cmd.Stdout, cmd.Stderr = w, w
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	w.Close()
	return commandResult(ctx, err)
}

// logWriter collects output and flushes it in chunks of at most
// protocol.MaxLogChunk bytes, at least every flushInterval, without
// splitting UTF-8 characters.
type logWriter struct {
	flush func(string)

	mu   sync.Mutex
	buf  []byte
	done chan struct{}
	wg   sync.WaitGroup
}

const flushInterval = 250 * time.Millisecond

func newLogWriter(flush func(string)) *logWriter {
	w := &logWriter{flush: flush, done: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		t := time.NewTicker(flushInterval)
		defer t.Stop()
		for {
			select {
			case <-w.done:
				return
			case <-t.C:
				w.mu.Lock()
				w.flushLocked(false)
				w.mu.Unlock()
			}
		}
	}()
	return w
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for len(w.buf) >= protocol.MaxLogChunk {
		w.flushLocked(false)
	}
	return len(p), nil
}

// Close stops the timer and sends whatever is left.
func (w *logWriter) Close() {
	close(w.done)
	w.wg.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked(true)
}

// flushLocked sends up to one chunk. Unless final, an incomplete UTF-8
// character at the end stays in the buffer for the next write.
func (w *logWriter) flushLocked(final bool) {
	for len(w.buf) > 0 {
		n := min(len(w.buf), protocol.MaxLogChunk)
		if n < len(w.buf) || !final {
			n = completeUTF8(w.buf[:n])
		}
		if n == 0 {
			if final {
				n = len(w.buf)
			} else {
				return
			}
		}
		w.flush(string(w.buf[:n]))
		w.buf = w.buf[n:]
		if !final {
			return
		}
	}
}

// completeUTF8 returns the length of b without a trailing incomplete UTF-8
// sequence.
func completeUTF8(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}
