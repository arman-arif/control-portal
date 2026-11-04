package protocol

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Site actions. Their params are a Site.
const (
	ActionSiteCreate = "site.create"
	ActionSiteUpdate = "site.update"
	ActionSiteDelete = "site.delete"
	// ActionSiteSSLIssue gets a certificate for the site's names and switches
	// it to HTTPS. Its params are the Site with SSL set.
	ActionSiteSSLIssue = "site.ssl.issue"
)

// Site is what the agent needs to set up, change or remove a site. Every
// field ends up in file paths or config files, so Validate is strict and both
// the panel and the agent call it.
type Site struct {
	Domain     string   `json:"domain"`
	Aliases    []string `json:"aliases"`
	SystemUser string   `json:"system_user"`
	WebDir     string   `json:"web_dir"` // relative to the site directory; "" serves the directory itself
	PHPVersion string   `json:"php_version"`
	// DeleteFiles, for site.delete only, also removes the system user and its
	// home directory with the site's files.
	DeleteFiles bool `json:"delete_files,omitempty"`
	// SSL sites are served over HTTPS with a certificate from ACME, which
	// the agent keeps covering the site's names.
	SSL  bool  `json:"ssl,omitempty"`
	ACME *ACME `json:"acme,omitempty"`
}

// ACME is where a site's certificate comes from.
type ACME struct {
	Email  string `json:"email"`
	Server string `json:"server,omitempty"` // directory URL; empty means Let's Encrypt
}

func (a ACME) Validate() error {
	addr, err := mail.ParseAddress(a.Email)
	if err != nil || addr.Address != a.Email || addr.Name != "" || strings.ContainsAny(a.Email, " \t\r\n\"'`$;&|<>()") || len(a.Email) > 254 {
		return fmt.Errorf("%q is not a plain email address", a.Email)
	}
	if a.Server != "" {
		u, err := url.Parse(a.Server)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(a.Server, " \t\r\n\"'`$;&|<>()") {
			return fmt.Errorf("ACME server %q must be an https URL", a.Server)
		}
	}
	return nil
}

const MaxAliases = 20

var (
	domainLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	systemUser    = regexp.MustCompile(`^k_[a-z0-9_]{1,30}$`)
	webDirSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)
)

// ValidateDomain accepts a lower-case host name with at least two labels, such
// as example.com or blog.example.co.uk. Internationalized names must be given
// in their xn-- form.
func ValidateDomain(d string) error {
	if len(d) > 253 {
		return errors.New("domain is too long")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q is not a domain name like example.com", d)
	}
	for _, l := range labels {
		if !domainLabel.MatchString(l) {
			return fmt.Errorf("%q is not a valid domain name: use lower-case letters, digits, hyphens and dots", d)
		}
	}
	if !strings.ContainsAny(labels[len(labels)-1], "abcdefghijklmnopqrstuvwxyz") {
		return fmt.Errorf("%q looks like an IP address, not a domain name", d)
	}
	return nil
}

// ValidateWebDir accepts a relative path such as public or web/dist, with no
// "." or ".." parts.
func ValidateWebDir(dir string) error {
	if dir == "" {
		return nil
	}
	if len(dir) > 128 {
		return errors.New("web directory is too long")
	}
	for _, seg := range strings.Split(dir, "/") {
		if !webDirSegment.MatchString(seg) {
			return fmt.Errorf("web directory %q must be a relative path like public or web/dist", dir)
		}
	}
	return nil
}

func (s Site) Validate() error {
	if err := ValidateDomain(s.Domain); err != nil {
		return err
	}
	if len(s.Aliases) > MaxAliases {
		return fmt.Errorf("a site can have at most %d aliases", MaxAliases)
	}
	for i, a := range s.Aliases {
		if err := ValidateDomain(a); err != nil {
			return fmt.Errorf("alias: %w", err)
		}
		if a == s.Domain || slices.Contains(s.Aliases[:i], a) {
			return fmt.Errorf("alias %s is listed twice", a)
		}
	}
	if !systemUser.MatchString(s.SystemUser) {
		return fmt.Errorf("system user %q is not a Keel site user", s.SystemUser)
	}
	if err := ValidateWebDir(s.WebDir); err != nil {
		return err
	}
	if !slices.Contains(PHPVersions, s.PHPVersion) {
		return fmt.Errorf("PHP version must be one of %v", PHPVersions)
	}
	if s.SSL {
		if s.ACME == nil {
			return errors.New("an SSL site needs ACME settings")
		}
		if err := s.ACME.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// CertName is the name of the site's certificate in certbot.
func (s Site) CertName() string { return "keel-" + s.Domain }

// Certificate is a Keel site certificate the agent found on its server.
type Certificate struct {
	Domain   string    `json:"domain"`
	Names    []string  `json:"names"`
	NotAfter time.Time `json:"not_after"`
}

// Names returns the domain followed by the aliases.
func (s Site) Names() []string {
	return append([]string{s.Domain}, s.Aliases...)
}

// HomeDir is the system user's home, and Dir the site's directory in it.
func (s Site) HomeDir() string { return "/home/" + s.SystemUser }
func (s Site) Dir() string     { return s.HomeDir() + "/" + s.Domain }

// Root is the directory Nginx serves.
func (s Site) Root() string {
	if s.WebDir == "" {
		return s.Dir()
	}
	return s.Dir() + "/" + s.WebDir
}

// SystemUserBase suggests a system user for a domain, such as k_example_com.
// The panel adds a suffix when another site on the server already has it.
func SystemUserBase(domain string) string {
	var b strings.Builder
	for _, r := range domain {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	base := b.String()
	if len(base) > 24 {
		base = base[:24]
	}
	return "k_" + strings.TrimRight(base, "_")
}

// ParseAliases splits a list typed by a person, separated by spaces, commas or
// new lines, and lower-cases it.
func ParseAliases(s string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r'
	}) {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}
