package protocol

import (
	"fmt"
	"regexp"
	"slices"
)

// Web stack components the agent can install.
const (
	ComponentNginx   = "nginx"
	ComponentPHP     = "php"
	ComponentMySQL   = "mysql"
	ComponentMariaDB = "mariadb"
	ComponentRedis   = "redis"
	ComponentNodeJS  = "nodejs"
	ComponentCertbot = "certbot"
)

// Components lists the installable components in display order.
var Components = []string{ComponentNginx, ComponentPHP, ComponentMySQL, ComponentMariaDB, ComponentRedis, ComponentNodeJS, ComponentCertbot}

// PHPVersions are the PHP versions Keel installs, from the ondrej/php PPA.
var PHPVersions = []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"}

// NodeVersions are the Node.js major versions Keel installs, from NodeSource.
// Only release lines still supported upstream are offered.
var NodeVersions = []string{"22", "24", "26"}

// StackInstall is the params of an ActionStackInstall job.
type StackInstall struct {
	Component string `json:"component"`
	Version   string `json:"version,omitempty"`
}

// Validate checks the component and that a version is given exactly when the
// component needs one.
func (p StackInstall) Validate() error {
	var versions []string
	switch p.Component {
	case ComponentPHP:
		versions = PHPVersions
	case ComponentNodeJS:
		versions = NodeVersions
	case ComponentNginx, ComponentMySQL, ComponentMariaDB, ComponentRedis, ComponentCertbot:
		if p.Version != "" {
			return fmt.Errorf("%s takes no version", p.Component)
		}
		return nil
	default:
		return fmt.Errorf("unknown component %q", p.Component)
	}
	if !slices.Contains(versions, p.Version) {
		return fmt.Errorf("%s version must be one of %v", p.Component, versions)
	}
	return nil
}

// Inventory is what the agent reports about the web stack on its server. Each
// string is the installed version, or empty when the component is missing.
type Inventory struct {
	Nginx   string   `json:"nginx"`
	PHP     []string `json:"php"` // installed PHP-FPM versions, such as "8.3"
	MySQL   string   `json:"mysql"`
	MariaDB string   `json:"mariadb"`
	Redis   string   `json:"redis"`
	NodeJS  string   `json:"nodejs"`
	Certbot string   `json:"certbot"`
	// Certificates are the certificates of Keel sites on the server.
	Certificates []Certificate `json:"certificates"`
}

const maxCertificates = 1000

var (
	phpVersionPattern  = regexp.MustCompile(`^[0-9]\.[0-9]$`)
	pkgVersionPattern  = regexp.MustCompile(`^[0-9A-Za-z.+~:-]{0,64}$`)
	nodeVersionPattern = regexp.MustCompile(`^([0-9]+)\.`)
)

// Clean drops anything that doesn't look like a version, so the panel only
// stores and shows plain values from an agent.
func (inv Inventory) Clean() Inventory {
	version := func(s string) string {
		if pkgVersionPattern.MatchString(s) {
			return s
		}
		return "?"
	}
	out := Inventory{
		Nginx: version(inv.Nginx), MySQL: version(inv.MySQL), MariaDB: version(inv.MariaDB),
		Redis: version(inv.Redis), NodeJS: version(inv.NodeJS), Certbot: version(inv.Certbot),
		PHP: []string{}, Certificates: []Certificate{},
	}
	for _, c := range inv.Certificates {
		if len(out.Certificates) == maxCertificates || ValidateDomain(c.Domain) != nil {
			continue
		}
		names := []string{}
		for _, n := range c.Names {
			if ValidateDomain(n) == nil && len(names) <= MaxAliases {
				names = append(names, n)
			}
		}
		out.Certificates = append(out.Certificates, Certificate{Domain: c.Domain, Names: names, NotAfter: c.NotAfter.UTC()})
	}
	for _, v := range inv.PHP {
		if phpVersionPattern.MatchString(v) && !slices.Contains(out.PHP, v) {
			out.PHP = append(out.PHP, v)
		}
	}
	slices.Sort(out.PHP)
	return out
}

// NodeMajor returns the major version of an installed Node.js version, such
// as "22" for "22.11.0", or "".
func NodeMajor(version string) string {
	if m := nodeVersionPattern.FindStringSubmatch(version); m != nil {
		return m[1]
	}
	return ""
}

// StackConflict explains why p can't be installed on a server with inv, or
// returns "" when it can. Installing something already there is allowed and
// does nothing harmful: apt just reports it as up to date.
func StackConflict(p StackInstall, inv Inventory) string {
	switch p.Component {
	case ComponentMySQL:
		if inv.MariaDB != "" {
			return "MariaDB is installed; a server runs MySQL or MariaDB, not both"
		}
	case ComponentMariaDB:
		if inv.MySQL != "" {
			return "MySQL is installed; a server runs MySQL or MariaDB, not both"
		}
	case ComponentNodeJS:
		if major := NodeMajor(inv.NodeJS); inv.NodeJS != "" && major != p.Version {
			return fmt.Sprintf("Node.js %s is installed; Keel doesn't switch Node.js versions", inv.NodeJS)
		}
	}
	return ""
}
