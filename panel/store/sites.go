package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Site statuses.
const (
	SiteCreating = "creating"
	SiteUpdating = "updating"
	SiteDeleting = "deleting"
	SiteActive   = "active"
	SiteFailed   = "failed"
)

var (
	ErrDomainTaken = errors.New("domain already used by a site on this server")
	errUserTaken   = errors.New("system user taken")
)

type Site struct {
	ID         int64    `json:"id"`
	ServerID   int64    `json:"server_id"`
	Domain     string   `json:"domain"`
	Aliases    []string `json:"aliases"`
	SystemUser string   `json:"system_user"`
	WebDir     string   `json:"web_dir"`
	PHPVersion string   `json:"php_version"`
	SSL        bool     `json:"ssl"`
	ACMEEmail  string   `json:"acme_email"`
	RepoURL    string   `json:"repo_url"`
	Branch     string   `json:"branch"`
	// DeployScript is nil while the site uses Keel's default script.
	DeployScript  *string `json:"-"`
	DeployTimeout int     `json:"deploy_timeout"`
	DeployKey     string  `json:"deploy_key"`
	UserID        int64   `json:"-"`
	// GitHubInstallationID is set when the site deploys through the GitHub App.
	GitHubInstallationID *int64    `json:"github_installation_id"`
	Status               string    `json:"status"`
	Error                string    `json:"error"`
	CreatedAt            time.Time `json:"created_at"`
}

const siteCols = `id, server_id, domain, aliases, unix_user, web_dir, php_version, ssl, acme_email, status, error, created_at,
	repo_url, branch, deploy_script, deploy_timeout, deploy_key, user_id, github_installation_id`

func scanSite(row pgx.Row) (Site, error) {
	var s Site
	err := row.Scan(&s.ID, &s.ServerID, &s.Domain, &s.Aliases, &s.SystemUser, &s.WebDir, &s.PHPVersion, &s.SSL, &s.ACMEEmail, &s.Status, &s.Error, &s.CreatedAt,
		&s.RepoURL, &s.Branch, &s.DeployScript, &s.DeployTimeout, &s.DeployKey, &s.UserID, &s.GitHubInstallationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	if s.Aliases == nil {
		s.Aliases = []string{}
	}
	return s, err
}

// NewSite is what CreateSite needs; the system user is picked from
// SystemUserBase, with a numeric suffix if another site on the server has it.
type NewSite struct {
	ServerID       int64
	Domain         string
	Aliases        []string
	SystemUserBase string
	WebDir         string
	PHPVersion     string
}

func (s *Store) CreateSite(ctx context.Context, userID int64, in NewSite) (Site, error) {
	for i := 1; i <= 50; i++ {
		name := in.SystemUserBase
		if i > 1 {
			name += "_" + strconv.Itoa(i)
		}
		site, err := scanSite(s.db.QueryRow(ctx, `INSERT INTO sites(server_id, user_id, domain, aliases, unix_user, web_dir, php_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+siteCols,
			in.ServerID, userID, in.Domain, in.Aliases, name, in.WebDir, in.PHPVersion))
		switch uniqueViolation(err) {
		case "sites_server_domain_key":
			return Site{}, ErrDomainTaken
		case "sites_server_unix_user_key":
			continue
		}
		return site, err
	}
	return Site{}, errUserTaken
}

func uniqueViolation(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}

func (s *Store) querySites(ctx context.Context, sql string, args ...any) ([]Site, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sites := []Site{}
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

func (s *Store) ListSites(ctx context.Context, userID, serverID int64) ([]Site, error) {
	return s.querySites(ctx, `SELECT `+siteCols+` FROM sites WHERE user_id=$1 AND server_id=$2 ORDER BY domain`, userID, serverID)
}

func (s *Store) GetSite(ctx context.Context, userID, id int64) (Site, error) {
	return scanSite(s.db.QueryRow(ctx, `SELECT `+siteCols+` FROM sites WHERE id=$1 AND user_id=$2`, id, userID))
}

// SiteNameInUse reports which of names another site on the server already
// serves, as its domain or an alias. It returns "" when none is.
func (s *Store) SiteNameInUse(ctx context.Context, serverID, exceptSiteID int64, names []string) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT n FROM sites, unnest($3::text[]) AS n
		WHERE server_id=$1 AND id<>$2 AND (domain=n OR n=ANY(aliases)) LIMIT 1`, serverID, exceptSiteID, names).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// UpdateSite saves new settings and marks the site as updating.
func (s *Store) UpdateSite(ctx context.Context, userID, id int64, aliases []string, webDir, phpVersion string) (Site, error) {
	return scanSite(s.db.QueryRow(ctx, `UPDATE sites SET aliases=$3, web_dir=$4, php_version=$5, status='updating', error=''
		WHERE id=$1 AND user_id=$2 RETURNING `+siteCols, id, userID, aliases, webDir, phpVersion))
}

func (s *Store) SetSiteStatus(ctx context.Context, userID, id int64, status string) (Site, error) {
	return scanSite(s.db.QueryRow(ctx, `UPDATE sites SET status=$3, error='' WHERE id=$1 AND user_id=$2 RETURNING `+siteCols,
		id, userID, status))
}

// SiteJobFinished records the outcome of a site's job, unless more of its
// jobs that change the site are still waiting, whose outcome will count
// instead. Deploys have their own history and don't count.
func (s *Store) SiteJobFinished(ctx context.Context, siteID int64, status, errMsg string) error {
	_, err := s.db.Exec(ctx, `UPDATE sites SET status=$2, error=$3 WHERE id=$1 AND NOT EXISTS
		(SELECT 1 FROM jobs WHERE site_id=$1 AND status IN ('queued', 'running')
			AND action NOT IN ('site.deploy', 'site.deploykey'))`, siteID, status, errMsg)
	return err
}

// DeleteSiteRow removes a site once the server no longer has it.
func (s *Store) DeleteSiteRow(ctx context.Context, siteID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM sites WHERE id=$1 AND NOT EXISTS
		(SELECT 1 FROM jobs WHERE site_id=$1 AND status IN ('queued', 'running'))`, siteID)
	return err
}

// StartSSL records the ACME email for a site whose certificate is about to be
// requested, and marks the site as updating.
func (s *Store) StartSSL(ctx context.Context, userID, id int64, email string) (Site, error) {
	return scanSite(s.db.QueryRow(ctx, `UPDATE sites SET acme_email=$3, status='updating', error=''
		WHERE id=$1 AND user_id=$2 RETURNING `+siteCols, id, userID, email))
}

// SiteSSLIssued marks a site as served over HTTPS.
func (s *Store) SiteSSLIssued(ctx context.Context, siteID int64) error {
	_, err := s.db.Exec(ctx, `UPDATE sites SET ssl=true WHERE id=$1`, siteID)
	return err
}

// GetSiteByID loads a site without checking its owner, for webhooks; the
// caller must authenticate the request some other way.
func (s *Store) GetSiteByID(ctx context.Context, id int64) (Site, error) {
	return scanSite(s.db.QueryRow(ctx, `SELECT `+siteCols+` FROM sites WHERE id=$1`, id))
}

// SetSiteRepo saves where a site deploys from. The webhook secret is set only
// if the site has none yet.
func (s *Store) SetSiteRepo(ctx context.Context, userID, id int64, repoURL, branch string, sealedSecret []byte) (Site, error) {
	return scanSite(s.db.QueryRow(ctx, `UPDATE sites SET repo_url=$3, branch=$4, webhook_secret=COALESCE(webhook_secret, $5)
		WHERE id=$1 AND user_id=$2 RETURNING `+siteCols, id, userID, repoURL, branch, sealedSecret))
}

// SetSiteDeployScript saves the deploy script; nil goes back to the default.
func (s *Store) SetSiteDeployScript(ctx context.Context, userID, id int64, script *string, timeoutMinutes int) (Site, error) {
	return scanSite(s.db.QueryRow(ctx, `UPDATE sites SET deploy_script=$3, deploy_timeout=$4
		WHERE id=$1 AND user_id=$2 RETURNING `+siteCols, id, userID, script, timeoutMinutes))
}

func (s *Store) SetSiteDeployKey(ctx context.Context, siteID int64, publicKey string) error {
	_, err := s.db.Exec(ctx, `UPDATE sites SET deploy_key=$2 WHERE id=$1`, siteID, publicKey)
	return err
}

// SiteWebhookSecret returns a site's sealed webhook secret, or ErrNotFound.
func (s *Store) SiteWebhookSecret(ctx context.Context, siteID int64) ([]byte, error) {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT webhook_secret FROM sites WHERE id=$1`, siteID).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && sealed == nil) {
		return nil, ErrNotFound
	}
	return sealed, err
}

// ---- deployments ----

type Deployment struct {
	ID            int64      `json:"id"`
	SiteID        int64      `json:"site_id"`
	JobID         *int64     `json:"job_id"`
	Trigger       string     `json:"trigger"`
	CommitSHA     string     `json:"commit_sha"`
	CommitMessage string     `json:"commit_message"`
	CreatedAt     time.Time  `json:"created_at"`
	Status        string     `json:"status"` // the job's status
	Error         string     `json:"error"`
	FinishedAt    *time.Time `json:"finished_at"`
}

func (s *Store) CreateDeployment(ctx context.Context, siteID, jobID int64, trigger, sha, message string) (Deployment, error) {
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO deployments(site_id, job_id, trigger, commit_sha, commit_message)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, siteID, jobID, trigger, sha, message).Scan(&id); err != nil {
		return Deployment{}, err
	}
	list, err := s.listDeployments(ctx, `d.id=$1`, id)
	if err != nil || len(list) == 0 {
		return Deployment{}, err
	}
	return list[0], nil
}

func (s *Store) ListDeployments(ctx context.Context, userID, siteID int64, limit int) ([]Deployment, error) {
	return s.listDeployments(ctx, `d.site_id=$1 AND EXISTS (SELECT 1 FROM sites WHERE id=d.site_id AND user_id=$2)
		ORDER BY d.id DESC LIMIT $3`, siteID, userID, limit)
}

func (s *Store) listDeployments(ctx context.Context, where string, args ...any) ([]Deployment, error) {
	rows, err := s.db.Query(ctx, `SELECT d.id, d.site_id, d.job_id, d.trigger, d.commit_sha, d.commit_message, d.created_at,
		COALESCE(j.status, 'failed'), COALESCE(j.error, ''), j.finished_at
		FROM deployments d LEFT JOIN jobs j ON j.id = d.job_id WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Deployment{}
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.SiteID, &d.JobID, &d.Trigger, &d.CommitSHA, &d.CommitMessage, &d.CreatedAt, &d.Status, &d.Error, &d.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDeploymentCommit records the commit a deploy job checked out.
func (s *Store) SetDeploymentCommit(ctx context.Context, jobID int64, sha, message string) error {
	_, err := s.db.Exec(ctx, `UPDATE deployments SET commit_sha=$2, commit_message=$3 WHERE job_id=$1`, jobID, sha, message)
	return err
}
