package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type GitHubApp struct {
	UserID    int64     `json:"-"`
	AppID     int64     `json:"app_id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	// Sealed with KEEL_SECRET_KEY.
	PrivateKey    []byte `json:"-"`
	WebhookSecret []byte `json:"-"`
}

const githubAppCols = `user_id, app_id, slug, name, html_url, created_at, private_key, webhook_secret`

func scanGitHubApp(row pgx.Row) (GitHubApp, error) {
	var a GitHubApp
	err := row.Scan(&a.UserID, &a.AppID, &a.Slug, &a.Name, &a.HTMLURL, &a.CreatedAt, &a.PrivateKey, &a.WebhookSecret)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// SaveGitHubApp stores an account's app, replacing any earlier one.
func (s *Store) SaveGitHubApp(ctx context.Context, a GitHubApp) error {
	_, err := s.db.Exec(ctx, `INSERT INTO github_apps(user_id, app_id, slug, name, html_url, private_key, webhook_secret)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id) DO UPDATE SET app_id=$2, slug=$3, name=$4, html_url=$5, private_key=$6, webhook_secret=$7, created_at=now()`,
		a.UserID, a.AppID, a.Slug, a.Name, a.HTMLURL, a.PrivateKey, a.WebhookSecret)
	return err
}

func (s *Store) GitHubAppByUser(ctx context.Context, userID int64) (GitHubApp, error) {
	return scanGitHubApp(s.db.QueryRow(ctx, `SELECT `+githubAppCols+` FROM github_apps WHERE user_id=$1`, userID))
}

// GitHubAppByAppID finds an app from a webhook; the caller must check the
// request's signature with the app's secret.
func (s *Store) GitHubAppByAppID(ctx context.Context, appID int64) (GitHubApp, error) {
	return scanGitHubApp(s.db.QueryRow(ctx, `SELECT `+githubAppCols+` FROM github_apps WHERE app_id=$1`, appID))
}

func (s *Store) DeleteGitHubApp(ctx context.Context, userID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM github_apps WHERE user_id=$1`, userID)
	return err
}

// SetSiteGitHubSource makes a site deploy a repository through the GitHub
// App's installation, or through a deploy key or public URL when
// installationID is nil.
func (s *Store) SetSiteGitHubSource(ctx context.Context, userID, id int64, installationID *int64) error {
	_, err := s.db.Exec(ctx, `UPDATE sites SET github_installation_id=$3 WHERE id=$1 AND user_id=$2`, id, userID, installationID)
	return err
}

// SitesForPush lists an account's sites that deploy the repository's branch
// through the given installation.
func (s *Store) SitesForPush(ctx context.Context, userID, installationID int64, repoURL, branch string) ([]Site, error) {
	return s.querySites(ctx, `SELECT `+siteCols+` FROM sites WHERE user_id=$1 AND github_installation_id=$2
		AND repo_url=$3 AND branch=$4`, userID, installationID, repoURL, branch)
}
