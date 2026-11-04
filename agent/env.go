package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// runAsUserWithInput is runAsUser with stdin.
var runAsUserWithInput = func(ctx context.Context, s protocol.Site, dir string, stdin string, name string, args ...string) (stdout, stderr string, err error) {
	uid, gid, err := siteOwner(s)
	if err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = userEnv(s)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// The scripts run as the site user, so a .env that is a symlink, or a site
// folder replaced by one, can only reach what the site could reach anyway.
const (
	envReadScript = `if [ ! -e .env ]; then exit 3; fi
if [ ! -f .env ]; then echo ".env is not a regular file" >&2; exit 4; fi
exec head -c 262145 .env`
	// A temporary file renamed over .env, so a reader never sees half a file.
	envWriteScript = `set -e
tmp=$(mktemp .env.keel-XXXXXX)
trap 'rm -f "$tmp"' EXIT
cat >"$tmp"
chmod 640 "$tmp"
mv -f "$tmp" .env
trap - EXIT`
)

// runEnvJob reads or writes a site's .env file. Nothing about the content
// goes to the job log; a read returns it in the result.
func runEnvJob(ctx context.Context, job protocol.Job) protocol.JobResult {
	var p protocol.EnvParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return protocol.JobResult{Error: "malformed params"}
	}
	if err := p.Validate(); err != nil {
		return protocol.JobResult{Error: err.Error()}
	}
	s := p.Site
	if _, err := lookupUser(s.SystemUser); err != nil {
		return protocol.JobResult{Error: "user " + s.SystemUser + " does not exist; is the site set up?"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch job.Action {
	case protocol.ActionSiteEnvRead:
		out, errOut, err := runAsUserWithInput(ctx, s, s.Dir(), "", "sh", "-c", envReadScript)
		var file protocol.EnvFile
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitCode() == 3:
			// No .env yet: an empty file to start from.
		case err != nil:
			return protocol.JobResult{Error: fmt.Sprintf("reading .env: %v %s", err, strings.TrimSpace(errOut))}
		case len(out) > protocol.MaxEnvFile:
			return protocol.JobResult{Error: "the .env file is larger than 256 KB"}
		default:
			file = protocol.EnvFile{Exists: true, Content: out}
		}
		result, _ := json.Marshal(file)
		return protocol.JobResult{OK: true, Result: result}
	case protocol.ActionSiteEnvWrite:
		if _, errOut, err := runAsUserWithInput(ctx, s, s.Dir(), p.Content, "sh", "-c", envWriteScript); err != nil {
			return protocol.JobResult{Error: fmt.Sprintf("writing .env: %v %s", err, strings.TrimSpace(errOut))}
		}
		return protocol.JobResult{OK: true}
	}
	return protocol.JobResult{Error: "unsupported action " + job.Action}
}
