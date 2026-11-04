package protocol

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Deploy actions.
const (
	// ActionSiteDeployKey makes sure the site user has an SSH key and returns
	// its public half as a DeployKeyResult. Params: Site.
	ActionSiteDeployKey = "site.deploykey"
	// ActionSiteDeploy checks out the branch in the site folder and runs the
	// deploy script, returning a DeployResult. Params: Deploy.
	ActionSiteDeploy = "site.deploy"
)

const (
	MaxDeployScript       = 64 << 10
	DefaultDeployTimeout  = 60 // minutes
	MaxDeployTimeout      = 180
	DeployKeyComment      = "keel"
	GitHubSSHPrefix       = "git@github.com:"
	GitHubHTTPSPrefix     = "https://github.com/"
	maxRepoPartLen        = 100
	DefaultDeployBranch   = "main"
	DefaultDeployScriptV1 = `# Runs as the site user in the site folder, after the new code is checked out.
# Any failing command stops the deploy. "php" is this site's PHP version.

if [ -f composer.json ]; then
    php /usr/bin/composer install --no-dev --no-interaction --prefer-dist --optimize-autoloader
fi

if [ -f artisan ]; then
    php artisan migrate --force
    php artisan optimize
fi
`
)

// Deploy is the params of a site.deploy job.
type Deploy struct {
	Site           Site   `json:"site"`
	RepoURL        string `json:"repo_url"`
	Branch         string `json:"branch"`
	Script         string `json:"script"`
	TimeoutMinutes int    `json:"timeout_minutes"`
	// Token, for an https repository, is a short-lived GitHub App token that
	// can read it. The panel adds it when the job is sent and never stores it.
	Token string `json:"token,omitempty"`
}

type DeployResult struct {
	Commit  string `json:"commit"`
	Message string `json:"message"`
}

type DeployKeyResult struct {
	PublicKey string `json:"public_key"`
}

var repoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// NormalizeRepoURL accepts a GitHub repository as git@github.com:owner/repo,
// https://github.com/owner/repo or owner/repo, with or without .git. SSH is
// the default: it uses the site's deploy key. The https form works only for
// public repositories. It returns the URL to clone.
func NormalizeRepoURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	https := strings.HasPrefix(s, GitHubHTTPSPrefix)
	switch {
	case strings.HasPrefix(s, GitHubSSHPrefix):
		s = strings.TrimPrefix(s, GitHubSSHPrefix)
	case https:
		s = strings.TrimPrefix(s, GitHubHTTPSPrefix)
	case strings.Contains(s, ":") || strings.HasPrefix(s, "/"):
		return "", errors.New("the repository must be on GitHub, like git@github.com:owner/repo.git")
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("%q is not a GitHub repository like owner/repo", raw)
	}
	for _, p := range parts {
		if !repoPart.MatchString(p) || len(p) > maxRepoPartLen || p == "." || p == ".." || strings.HasPrefix(p, "-") {
			return "", fmt.Errorf("%q is not a GitHub repository like owner/repo", raw)
		}
	}
	if https {
		return GitHubHTTPSPrefix + parts[0] + "/" + parts[1] + ".git", nil
	}
	return GitHubSSHPrefix + parts[0] + "/" + parts[1] + ".git", nil
}

// RepoUsesSSH reports whether a normalized repository URL needs the deploy key.
func RepoUsesSSH(url string) bool { return strings.HasPrefix(url, GitHubSSHPrefix) }

var branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)

// ValidateBranch accepts the branch names git allows, minus a few odd ones.
func ValidateBranch(b string) error {
	if !branchPattern.MatchString(b) || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "..") || strings.Contains(b, "//") ||
		strings.Contains(b, "/.") || strings.HasPrefix(b, ".") {
		return fmt.Errorf("%q is not a valid branch name", b)
	}
	return nil
}

func (d Deploy) Validate() error {
	if err := d.Site.Validate(); err != nil {
		return err
	}
	if url, err := NormalizeRepoURL(d.RepoURL); err != nil {
		return err
	} else if url != d.RepoURL {
		return fmt.Errorf("repository URL %q is not normalized", d.RepoURL)
	}
	if err := ValidateBranch(d.Branch); err != nil {
		return err
	}
	if len(d.Script) > MaxDeployScript {
		return fmt.Errorf("the deploy script is longer than %d KB", MaxDeployScript>>10)
	}
	if d.TimeoutMinutes < 1 || d.TimeoutMinutes > MaxDeployTimeout {
		return fmt.Errorf("the deploy timeout must be 1 to %d minutes", MaxDeployTimeout)
	}
	if d.Token != "" && (RepoUsesSSH(d.RepoURL) || !tokenPattern.MatchString(d.Token)) {
		return errors.New("a GitHub token goes only with an https repository, and must be letters, digits and underscores")
	}
	return nil
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)
