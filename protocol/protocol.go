// Package protocol defines the messages exchanged between the panel and the
// agent over their WebSocket connection. Both sides import it so the wire
// format stays in sync.
package protocol

import (
	"encoding/json"
	"regexp"
)

// Message types.
const (
	TypeHello     = "hello"      // agent -> panel, once after connecting
	TypeStats     = "stats"      // agent -> panel, periodically
	TypeServices  = "services"   // agent -> panel, periodically and after a job
	TypeJob       = "job"        // panel -> agent
	TypeJobResult = "job_result" // agent -> panel, reply to a job with the same ID
)

// Job actions the agent accepts. The agent rejects anything else.
const (
	ActionServiceStart   = "service.start"
	ActionServiceStop    = "service.stop"
	ActionServiceRestart = "service.restart"
)

// Envelope wraps every message on the wire.
type Envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Encode builds a serialized envelope around v.
func Encode(typ, id string, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{Type: typ, ID: id, Data: data})
}

type Hello struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
	CPUs         int    `json:"cpus"`
	AgentVersion string `json:"agent_version"`
}

type Stats struct {
	Time       int64   `json:"time"` // unix seconds
	CPUPercent float64 `json:"cpu_percent"`
	MemTotal   uint64  `json:"mem_total"`
	MemUsed    uint64  `json:"mem_used"`
	DiskTotal  uint64  `json:"disk_total"`
	DiskUsed   uint64  `json:"disk_used"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	Uptime     uint64  `json:"uptime"` // seconds
}

type Service struct {
	Name   string `json:"name"`
	Active string `json:"active"` // active, inactive, failed, ...
	Sub    string `json:"sub"`    // running, dead, exited, ...
}

type Job struct {
	Action  string `json:"action"`
	Service string `json:"service,omitempty"`
}

type JobResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

var managedServices = regexp.MustCompile(`^(nginx|apache2|mysql|mariadb|postgresql|redis-server|memcached|php[0-9]\.[0-9]-fpm|supervisor|cron|ssh|ufw|fail2ban|docker)$`)

// IsManagedService reports whether the panel may show and control a systemd
// service. The list is fixed so the panel can never be used to touch
// arbitrary units.
func IsManagedService(name string) bool {
	return managedServices.MatchString(name)
}

// IsServiceAction reports whether action is one of the service job actions.
func IsServiceAction(action string) bool {
	switch action {
	case ActionServiceStart, ActionServiceStop, ActionServiceRestart:
		return true
	}
	return false
}
