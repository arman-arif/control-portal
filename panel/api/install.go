package api

import (
	_ "embed"
	"net/http"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed install.sh.tmpl
var installScriptSrc string

var installScript = template.Must(template.New("install.sh").Parse(installScriptSrc))

// agentBinaries are the only files served from /downloads/.
var agentBinaries = map[string]bool{
	"keel-agent-linux-amd64": true,
	"keel-agent-linux-arm64": true,
}

func (a *API) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	installScript.Execute(w, map[string]string{"PanelURL": a.cfg.PublicURL})
}

func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !agentBinaries[name] || strings.ContainsAny(name, `/\`) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, filepath.Join(a.cfg.AgentDir, name))
}
