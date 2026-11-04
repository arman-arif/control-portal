package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/arman-arif/control-portal/panel/store"
	"github.com/arman-arif/control-portal/protocol"
)

type siteView struct {
	store.Site
	Dir  string `json:"dir"`  // the site's directory on the server
	Root string `json:"root"` // the directory Nginx serves
}

func siteParams(s store.Site) protocol.Site {
	return protocol.Site{Domain: s.Domain, Aliases: s.Aliases, SystemUser: s.SystemUser, WebDir: s.WebDir, PHPVersion: s.PHPVersion}
}

func viewSite(s store.Site) siteView {
	p := siteParams(s)
	return siteView{Site: s, Dir: p.Dir(), Root: p.Root()}
}

// siteInput is the body of create and update requests. Pointers tell a field
// left out from one set to empty.
type siteInput struct {
	Domain     string    `json:"domain"`
	Aliases    *[]string `json:"aliases"`
	WebDir     *string   `json:"web_dir"`
	PHPVersion *string   `json:"php_version"`
}

func cleanNames(names []string) []string {
	out := []string{}
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// checkSiteFits refuses a site the server can't serve as far as the panel
// knows: the agent checks again when the job runs.
func (a *API) checkSiteFits(w http.ResponseWriter, r *http.Request, sv store.Server, p protocol.Site, siteID int64) bool {
	if !sv.Linked {
		writeError(w, http.StatusConflict, "this server is not linked yet")
		return false
	}
	if inv, ok := a.hub.Inventory(sv.ID); ok {
		if inv.Nginx == "" {
			writeError(w, http.StatusConflict, "Nginx is not installed on this server; install it from the Stack tab")
			return false
		}
		if !slices.Contains(inv.PHP, p.PHPVersion) {
			writeError(w, http.StatusConflict, "PHP "+p.PHPVersion+" is not installed on this server; install it from the Stack tab")
			return false
		}
	}
	name, err := a.store.SiteNameInUse(r.Context(), sv.ID, siteID, p.Names())
	if err != nil {
		internalError(w, err)
		return false
	}
	if name != "" {
		writeError(w, http.StatusConflict, name+" is already used by another site on this server")
		return false
	}
	return true
}

func (a *API) handleListSites(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	sites, err := a.store.ListSites(r.Context(), userFrom(r).ID, sv.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	out := make([]siteView, len(sites))
	for i, s := range sites {
		out[i] = viewSite(s)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	sv, ok := a.serverFromPath(w, r)
	if !ok {
		return
	}
	var in siteInput
	if !readJSON(w, r, &in) {
		return
	}
	p := protocol.Site{Domain: strings.ToLower(strings.TrimSpace(in.Domain)), Aliases: []string{}, WebDir: "public"}
	if in.Aliases != nil {
		p.Aliases = cleanNames(*in.Aliases)
	}
	if in.WebDir != nil {
		p.WebDir = strings.Trim(strings.TrimSpace(*in.WebDir), "/")
	}
	if in.PHPVersion != nil {
		p.PHPVersion = *in.PHPVersion
	}
	p.SystemUser = protocol.SystemUserBase(p.Domain)
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.checkSiteFits(w, r, sv, p, 0) {
		return
	}
	userID := userFrom(r).ID
	site, err := a.store.CreateSite(r.Context(), userID, store.NewSite{
		ServerID: sv.ID, Domain: p.Domain, Aliases: p.Aliases, SystemUserBase: p.SystemUser, WebDir: p.WebDir, PHPVersion: p.PHPVersion,
	})
	if errors.Is(err, store.ErrDomainTaken) {
		writeError(w, http.StatusConflict, p.Domain+" already has a site on this server")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	job, err := a.jobs.Enqueue(r.Context(), userID, sv.ID, &site.ID, protocol.ActionSiteCreate, siteParams(site))
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": viewSite(site), "job": job})
}

// siteFromPath loads the site named by {id} for the current user, writing an
// error response if it cannot.
func (a *API) siteFromPath(w http.ResponseWriter, r *http.Request) (store.Site, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "site not found")
		return store.Site{}, false
	}
	site, err := a.store.GetSite(r.Context(), userFrom(r).ID, id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "site not found")
		return site, false
	} else if err != nil {
		internalError(w, err)
		return site, false
	}
	return site, true
}

func (a *API) handleGetSite(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	jobs, err := a.store.ListSiteJobs(r.Context(), userFrom(r).ID, site.ID, 20)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		siteView
		Jobs []store.Job `json:"jobs"`
	}{viewSite(site), jobs})
}

func (a *API) handleUpdateSite(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	if site.Status == store.SiteDeleting {
		writeError(w, http.StatusConflict, "this site is being deleted")
		return
	}
	var in siteInput
	if !readJSON(w, r, &in) {
		return
	}
	if in.Domain != "" && in.Domain != site.Domain {
		writeError(w, http.StatusBadRequest, "a site's domain can't be changed; add the new name as an alias, or create a new site")
		return
	}
	p := siteParams(site)
	if in.Aliases != nil {
		p.Aliases = cleanNames(*in.Aliases)
	}
	if in.WebDir != nil {
		p.WebDir = strings.Trim(strings.TrimSpace(*in.WebDir), "/")
	}
	if in.PHPVersion != nil {
		p.PHPVersion = *in.PHPVersion
	}
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sv, err := a.store.GetServer(r.Context(), userFrom(r).ID, site.ServerID)
	if err != nil {
		internalError(w, err)
		return
	}
	if !a.checkSiteFits(w, r, sv, p, site.ID) {
		return
	}
	site, err = a.store.UpdateSite(r.Context(), userFrom(r).ID, site.ID, p.Aliases, p.WebDir, p.PHPVersion)
	if err != nil {
		internalError(w, err)
		return
	}
	job, err := a.jobs.Enqueue(r.Context(), userFrom(r).ID, site.ServerID, &site.ID, protocol.ActionSiteUpdate, siteParams(site))
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": viewSite(site), "job": job})
}

// handleDeleteSite removes the site from the server, and with
// ?delete_files=true its system user and files too. The panel forgets the
// site once the server has done that.
func (a *API) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	p := siteParams(site)
	p.DeleteFiles = r.URL.Query().Get("delete_files") == "true"
	site, err := a.store.SetSiteStatus(r.Context(), userFrom(r).ID, site.ID, store.SiteDeleting)
	if err != nil {
		internalError(w, err)
		return
	}
	job, err := a.jobs.Enqueue(r.Context(), userFrom(r).ID, site.ServerID, &site.ID, protocol.ActionSiteDelete, p)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": viewSite(site), "job": job})
}

// jobFinished keeps a site's status in step with its jobs.
func (a *API) jobFinished(ctx context.Context, job store.Job) {
	if job.SiteID == nil {
		return
	}
	ok := job.Status == store.JobSucceeded
	var err error
	switch {
	case job.Action == protocol.ActionSiteDelete && ok:
		err = a.store.DeleteSiteRow(ctx, *job.SiteID)
	case !ok:
		err = a.store.SiteJobFinished(ctx, *job.SiteID, store.SiteFailed, job.Error)
	default:
		err = a.store.SiteJobFinished(ctx, *job.SiteID, store.SiteActive, "")
	}
	if err != nil {
		log.Printf("updating site %d after job %d: %v", *job.SiteID, job.ID, err)
	}
}
