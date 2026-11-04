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
	ID         int64     `json:"id"`
	ServerID   int64     `json:"server_id"`
	Domain     string    `json:"domain"`
	Aliases    []string  `json:"aliases"`
	SystemUser string    `json:"system_user"`
	WebDir     string    `json:"web_dir"`
	PHPVersion string    `json:"php_version"`
	SSL        bool      `json:"ssl"`
	ACMEEmail  string    `json:"acme_email"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
	CreatedAt  time.Time `json:"created_at"`
}

const siteCols = `id, server_id, domain, aliases, unix_user, web_dir, php_version, ssl, acme_email, status, error, created_at`

func scanSite(row pgx.Row) (Site, error) {
	var s Site
	err := row.Scan(&s.ID, &s.ServerID, &s.Domain, &s.Aliases, &s.SystemUser, &s.WebDir, &s.PHPVersion, &s.SSL, &s.ACMEEmail, &s.Status, &s.Error, &s.CreatedAt)
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
// jobs are still waiting, whose outcome will count instead.
func (s *Store) SiteJobFinished(ctx context.Context, siteID int64, status, errMsg string) error {
	_, err := s.db.Exec(ctx, `UPDATE sites SET status=$2, error=$3 WHERE id=$1 AND NOT EXISTS
		(SELECT 1 FROM jobs WHERE site_id=$1 AND status IN ('queued', 'running'))`, siteID, status, errMsg)
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
