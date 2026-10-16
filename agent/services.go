package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// listServices returns the managed systemd services present on this machine.
func listServices(ctx context.Context) ([]protocol.Service, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain", "--no-pager").Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl list-units: %w", err)
	}
	return parseServiceList(string(out)), nil
}

// parseServiceList parses `systemctl list-units --plain --no-legend` output:
// UNIT LOAD ACTIVE SUB DESCRIPTION...
func parseServiceList(out string) []protocol.Service {
	services := []protocol.Service{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		name, ok := strings.CutSuffix(f[0], ".service")
		if !ok || f[1] != "loaded" || !protocol.IsManagedService(name) {
			continue
		}
		services = append(services, protocol.Service{Name: name, Active: f[2], Sub: f[3]})
	}
	return services
}

// runServiceJob performs a start, stop or restart on a managed service.
func runServiceJob(ctx context.Context, job protocol.Job) protocol.JobResult {
	if !protocol.IsServiceAction(job.Action) {
		return protocol.JobResult{Error: "unsupported action " + job.Action}
	}
	if !protocol.IsManagedService(job.Service) {
		return protocol.JobResult{Error: "service is not managed by Keel: " + job.Service}
	}
	verb := strings.TrimPrefix(job.Action, "service.")
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", verb, job.Service+".service").CombinedOutput()
	if err != nil {
		return protocol.JobResult{Output: string(out), Error: err.Error()}
	}
	return protocol.JobResult{OK: true, Output: string(out)}
}
