package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// step is one part of a recipe: a command to run, or Go code for work such as
// writing a file. Recipes are fixed here in the agent; the panel only picks
// one and its validated version.
type step struct {
	title string
	args  []string
	fn    func(ctx context.Context, out io.Writer) error
}

func cmdStep(args ...string) step {
	return step{title: strings.Join(args, " "), args: args}
}

var aptOptions = []string{
	"-o", "DPkg::Lock::Timeout=600", // wait for unattended-upgrades instead of failing
	"-o", "Dpkg::Options::=--force-confdef",
	"-o", "Dpkg::Options::=--force-confold",
}

func aptUpdate() step { return cmdStep("apt-get", "update") }

// aptInstall's title leaves out the dpkg options to keep the log readable.
func aptInstall(pkgs ...string) step {
	s := cmdStep(append([]string{"apt-get", "install", "-y"}, pkgs...)...)
	s.args = append(append([]string{"apt-get", "install", "-y"}, aptOptions...), pkgs...)
	return s
}

// commandEnv is the environment for recipe commands: no prompts, and
// needrestart only lists services instead of restarting them.
func commandEnv() []string {
	return append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l", "LC_ALL=C.UTF-8")
}

// phpExtensions are installed with every PHP version: what a typical Laravel
// or WordPress site needs.
var phpExtensions = []string{"fpm", "cli", "common", "mysql", "sqlite3", "curl", "mbstring", "xml", "zip", "bcmath", "intl", "gd", "opcache", "readline", "redis"}

// stackRecipe returns the steps that install a validated component.
func stackRecipe(p protocol.StackInstall) []step {
	switch p.Component {
	case protocol.ComponentNginx:
		return []step{aptUpdate(), aptInstall("nginx")}
	case protocol.ComponentPHP:
		pkgs := make([]string, 0, len(phpExtensions)+1)
		for _, ext := range phpExtensions {
			pkgs = append(pkgs, "php"+p.Version+"-"+ext)
		}
		if p.Version == "7.4" {
			pkgs = append(pkgs, "php7.4-json") // built in from PHP 8.0
		}
		return []step{
			aptUpdate(),
			aptInstall("software-properties-common"),
			cmdStep("add-apt-repository", "-y", "--no-update", "ppa:ondrej/php"),
			aptUpdate(),
			aptInstall(pkgs...),
		}
	case protocol.ComponentMySQL:
		return []step{aptUpdate(), aptInstall("mysql-server")}
	case protocol.ComponentMariaDB:
		return []step{aptUpdate(), aptInstall("mariadb-server")}
	case protocol.ComponentRedis:
		return []step{aptUpdate(), aptInstall("redis-server")}
	case protocol.ComponentNodeJS:
		return []step{
			aptUpdate(),
			aptInstall("ca-certificates"),
			{title: "add the NodeSource signing key", fn: func(ctx context.Context, _ io.Writer) error { return installNodeSourceKey(ctx) }},
			{title: "add the NodeSource Node.js " + p.Version + " repository", fn: func(context.Context, io.Writer) error { return writeNodeSourceRepo(p.Version) }},
			aptUpdate(),
			aptInstall("nodejs"),
		}
	case protocol.ComponentCertbot:
		return []step{aptUpdate(), aptInstall("certbot")}
	}
	return nil
}

// Where NodeSource's repository files go. Variables so tests can redirect them.
var (
	aptDir           = "/etc/apt"
	nodeSourceKeyURL = "https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key"
)

// installNodeSourceKey downloads NodeSource's signing key over HTTPS. apt reads
// an armored key directly when the file name ends in .asc, so gpg isn't needed.
func installNodeSourceKey(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeSourceKeyURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading the NodeSource key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the NodeSource key: %s", resp.Status)
	}
	key, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return err
	}
	if !bytes.Contains(key, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		return errors.New("the NodeSource key download is not a PGP public key")
	}
	return writeFileAtomic(filepath.Join(aptDir, "keyrings", "nodesource.asc"), key)
}

// writeNodeSourceRepo points apt at one Node.js major version and prefers it
// over Ubuntu's own, older nodejs package.
func writeNodeSourceRepo(major string) error {
	list := fmt.Sprintf("deb [signed-by=%s] https://deb.nodesource.com/node_%s.x nodistro main\n",
		filepath.Join(aptDir, "keyrings", "nodesource.asc"), major)
	if err := writeFileAtomic(filepath.Join(aptDir, "sources.list.d", "nodesource.list"), []byte(list)); err != nil {
		return err
	}
	pin := "Package: nodejs\nPin: origin deb.nodesource.com\nPin-Priority: 600\n"
	return writeFileAtomic(filepath.Join(aptDir, "preferences.d", "nodesource"), []byte(pin))
}

func writeFileAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".keel-tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// runSteps runs a recipe in order, streaming each step's output to the panel
// under a heading, and stops at the first failure.
func runSteps(ctx context.Context, send sendFunc, id string, steps []step) protocol.JobResult {
	w := newLogWriter(func(text string) { send(protocol.TypeJobLog, id, protocol.JobLog{Text: text}) })
	defer w.Close()
	for _, s := range steps {
		fmt.Fprintf(w, "==> %s\n", s.title)
		if s.fn != nil {
			if err := s.fn(ctx, w); err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return protocol.JobResult{Error: "the job took too long and was stopped"}
				}
				fmt.Fprintf(w, "error: %v\n", err)
				return protocol.JobResult{Error: s.title + ": " + err.Error()}
			}
			continue
		}
		cmd := exec.CommandContext(ctx, s.args[0], s.args[1:]...)
		cmd.Env = commandEnv()
		cmd.Stdout, cmd.Stderr = w, w
		cmd.WaitDelay = 5 * time.Second
		if res := commandResult(ctx, cmd.Run()); !res.OK {
			res.Error = s.title + ": " + res.Error
			return res
		}
	}
	return protocol.JobResult{OK: true}
}

// commandResult turns the error from running a command into a job result.
func commandResult(ctx context.Context, err error) protocol.JobResult {
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return protocol.JobResult{Error: "the job took too long and was stopped"}
	case err != nil:
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return protocol.JobResult{Error: fmt.Sprintf("command exited with status %d", exit.ExitCode())}
		}
		return protocol.JobResult{Error: err.Error()}
	}
	return protocol.JobResult{OK: true}
}

// inventoryPackages are the packages whose installed versions make up the
// inventory. dpkg-query expands the pattern itself.
var inventoryPackages = []string{"nginx", "mysql-server", "mariadb-server", "redis-server", "nodejs", "certbot", "php*-fpm"}

// collectInventory asks dpkg which stack packages are installed.
func collectInventory(ctx context.Context) (protocol.Inventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	args := append([]string{"-W", "-f=${Package}\t${db:Status-Abbrev}\t${Version}\n"}, inventoryPackages...)
	out, err := exec.CommandContext(ctx, "dpkg-query", args...).Output()
	// dpkg-query exits with 1 when some packages are unknown, but still lists
	// the ones it found.
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
		return protocol.Inventory{}, fmt.Errorf("dpkg-query: %w", err)
	}
	return parseInventory(string(out)), nil
}

// parseInventory reads dpkg-query lines of package, status and version.
func parseInventory(out string) protocol.Inventory {
	inv := protocol.Inventory{PHP: []string{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || !strings.HasPrefix(f[1], "ii") {
			continue
		}
		pkg, version := f[0], upstreamVersion(f[2])
		switch pkg {
		case "nginx":
			inv.Nginx = version
		case "mysql-server":
			inv.MySQL = version
		case "mariadb-server":
			inv.MariaDB = version
		case "redis-server":
			inv.Redis = version
		case "nodejs":
			inv.NodeJS = version
		case "certbot":
			inv.Certbot = version
		default:
			if v, ok := strings.CutPrefix(pkg, "php"); ok {
				if v, ok := strings.CutSuffix(v, "-fpm"); ok {
					inv.PHP = append(inv.PHP, v)
				}
			}
		}
	}
	return inv.Clean()
}

// upstreamVersion drops the Debian epoch, revision and packaging suffixes,
// for example "1:10.11.8-0ubuntu0.24.04.1" becomes "10.11.8".
func upstreamVersion(v string) string {
	if _, after, ok := strings.Cut(v, ":"); ok {
		v = after
	}
	if i := strings.LastIndex(v, "-"); i > 0 {
		v = v[:i]
	}
	if i := strings.IndexAny(v, "+~"); i > 0 {
		v = v[:i]
	}
	return v
}
