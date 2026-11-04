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
	// Certificate is what the server last reported for the site, if anything.
	Certificate *protocol.Certificate `json:"certificate"`
}

// siteParams is what the agent gets for a site.
func (a *API) siteParams(s store.Site) protocol.Site {
	p := protocol.Site{Domain: s.Domain, Aliases: s.Aliases, SystemUser: s.SystemUser, WebDir: s.WebDir, PHPVersion: s.PHPVersion, SSL: s.SSL}
	if s.SSL || s.ACMEEmail != "" {
		p.ACME = &protocol.ACME{Email: s.ACMEEmail, Server: a.cfg.ACMEServer}
	}
	return p
}

func (a *API) viewSite(s store.Site) siteView {
	p := a.siteParams(s)
	v := siteView{Site: s, Dir: p.Dir(), Root: p.Root()}
	if inv, ok := a.hub.Inventory(s.ServerID); ok {
		for _, c := range inv.Certificates {
			if c.Domain == s.Domain {
				v.Certificate = &c
				break
			}
		}
	}
	return v
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
		out[i] = a.viewSite(s)
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
	job, err := a.jobs.Enqueue(r.Context(), userID, sv.ID, store.SiteRef(site.ID), protocol.ActionSiteCreate, a.siteParams(site))
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": a.viewSite(site), "job": job})
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
	}{a.viewSite(site), jobs})
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
	p := a.siteParams(site)
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
	job, err := a.jobs.Enqueue(r.Context(), userFrom(r).ID, site.ServerID, store.SiteRef(site.ID), protocol.ActionSiteUpdate, a.siteParams(site))
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": a.viewSite(site), "job": job})
}

// handleDeleteSite removes the site from the server, and with
// ?delete_files=true its system user and files too. The panel forgets the
// site once the server has done that.
func (a *API) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	p := a.siteParams(site)
	p.DeleteFiles = r.URL.Query().Get("delete_files") == "true"
	site, err := a.store.SetSiteStatus(r.Context(), userFrom(r).ID, site.ID, store.SiteDeleting)
	if err != nil {
		internalError(w, err)
		return
	}
	job, err := a.jobs.Enqueue(r.Context(), userFrom(r).ID, site.ServerID, store.SiteRef(site.ID), protocol.ActionSiteDelete, p)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": a.viewSite(site), "job": job})
}

// handleIssueSSL gets a Let's Encrypt certificate for the site and switches it
// to HTTPS. The body's email, defaulting to the account's, is the ACME
// contact for expiry notices.
func (a *API) handleIssueSSL(w http.ResponseWriter, r *http.Request) {
	site, ok := a.siteFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	email := strings.TrimSpace(in.Email)
	if email == "" {
		email = userFrom(r).Email
	}
	if site.Status == store.SiteDeleting {
		writeError(w, http.StatusConflict, "this site is being deleted")
		return
	}
	p := a.siteParams(site)
	p.SSL, p.ACME = true, &protocol.ACME{Email: email, Server: a.cfg.ACMEServer}
	if err := p.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if inv, ok := a.hub.Inventory(site.ServerID); ok && inv.Certbot == "" {
		writeError(w, http.StatusConflict, "Certbot is not installed on this server; install it from the Stack tab")
		return
	}
	site, err := a.store.StartSSL(r.Context(), userFrom(r).ID, site.ID, email)
	if err != nil {
		internalError(w, err)
		return
	}
	job, err := a.jobs.Enqueue(r.Context(), userFrom(r).ID, site.ServerID, store.SiteRef(site.ID), protocol.ActionSiteSSLIssue, p)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": a.viewSite(site), "job": job})
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
	case job.Action == protocol.ActionSiteSSLIssue && ok:
		err = errors.Join(a.store.SiteSSLIssued(ctx, *job.SiteID), a.store.SiteJobFinished(ctx, *job.SiteID, store.SiteActive, ""))
	case !ok:
		err = a.store.SiteJobFinished(ctx, *job.SiteID, store.SiteFailed, job.Error)
	default:
		err = a.store.SiteJobFinished(ctx, *job.SiteID, store.SiteActive, "")
	}
	if err != nil {
		log.Printf("updating site %d after job %d: %v", *job.SiteID, job.ID, err)
	}
}
