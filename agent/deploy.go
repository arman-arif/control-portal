package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// githubKnownHosts pins GitHub's SSH host keys, as published at
// https://api.github.com/meta, so a deploy never trusts a key it is shown.
const githubKnownHosts = `github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
`

// runAsUser runs a command as the site user, with only the environment
// given. The deploy script and git never run as root.
var runAsUser = func(ctx context.Context, out io.Writer, s protocol.Site, dir string, env []string, name string, args ...string) (string, error) {
	uid, gid, err := siteOwner(s)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.MultiWriter(out, &stdout), out
	cmd.WaitDelay = 10 * time.Second
	err = cmd.Run()
	return stdout.String(), err
}

func sshDir(s protocol.Site) string        { return s.HomeDir() + "/.ssh" }
func deployKeyPath(s protocol.Site) string { return sshDir(s) + "/id_ed25519" }
func knownHostsPath(s protocol.Site) string {
	return sshDir(s) + "/keel_known_hosts"
}

// phpShimDir holds a "php" that runs the given version, put first on the
// deploy script's PATH.
func phpShimDir(version string) string { return "/usr/local/lib/keel/php" + version }

// userEnv is the whole environment for commands run as the site user.
func userEnv(s protocol.Site, extra ...string) []string {
	return append([]string{
		"HOME=" + s.HomeDir(),
		"USER=" + s.SystemUser,
		"LOGNAME=" + s.SystemUser,
		"PATH=" + phpShimDir(s.PHPVersion) + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"NO_COLOR=1", // logs are read in a browser, not a terminal
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -i " + deployKeyPath(s) + " -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + knownHostsPath(s),
		"KEEL_DOMAIN=" + s.Domain,
		"KEEL_SITE_DIR=" + s.Dir(),
		"KEEL_PHP=/usr/bin/php" + s.PHPVersion,
	}, extra...)
}

// ensureOwnedDir makes a directory in the site user's home, owned by them.
func ensureOwnedDir(s protocol.Site, dir string, mode os.FileMode) error {
	uid, gid, err := siteOwner(s)
	if err != nil {
		return err
	}
	if err := os.Mkdir(rooted(dir), mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := os.Lchown(rooted(dir), uid, gid); err != nil {
		return err
	}
	return os.Chmod(rooted(dir), mode)
}

func writeOwnedFile(s protocol.Site, path string, content []byte, mode os.FileMode) error {
	uid, gid, err := siteOwner(s)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(rooted(path), content); err != nil {
		return err
	}
	if err := os.Lchown(rooted(path), uid, gid); err != nil {
		return err
	}
	return os.Chmod(rooted(path), mode)
}

// runDeployKey makes sure the site user has an SSH key and returns its public
// half.
func runDeployKey(ctx context.Context, send sendFunc, id string, s protocol.Site) protocol.JobResult {
	var pub string
	res := runSteps(ctx, send, id, []step{{title: "set up the deploy key of " + s.SystemUser, fn: func(ctx context.Context, out io.Writer) error {
		if _, err := lookupUser(s.SystemUser); err != nil {
			return fmt.Errorf("user %s does not exist; is the site set up?", s.SystemUser)
		}
		if err := ensureOwnedDir(s, sshDir(s), 0o700); err != nil {
			return err
		}
		if _, err := os.Stat(rooted(deployKeyPath(s))); errors.Is(err, fs.ErrNotExist) {
			if _, err := runAsUser(ctx, out, s, s.HomeDir(), userEnv(s), "ssh-keygen", "-q", "-t", "ed25519", "-N", "",
				"-C", protocol.DeployKeyComment+"-"+s.Domain, "-f", deployKeyPath(s)); err != nil {
				return fmt.Errorf("ssh-keygen: %w", err)
			}
			fmt.Fprintln(out, "created a new key")
		} else {
			fmt.Fprintln(out, "the key already exists")
		}
		b, err := os.ReadFile(rooted(deployKeyPath(s) + ".pub"))
		if err != nil {
			return err
		}
		pub = strings.TrimSpace(string(b))
		return nil
	}}})
	if res.OK {
		res.Result, _ = json.Marshal(protocol.DeployKeyResult{PublicKey: pub})
	}
	return res
}

// keelPlaceholder starts the index.php Keel puts in a new site.
const keelPlaceholder = "<?php\n// Placeholder from Keel."

func runDeploy(ctx context.Context, send sendFunc, id string, d protocol.Deploy) protocol.JobResult {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(d.TimeoutMinutes)*time.Minute)
	defer cancel()
	s := d.Site
	var result protocol.DeployResult
	gitEnv := userEnv(s)
	if d.Token != "" {
		// In the environment rather than the URL or arguments, so it shows up
		// in neither the log nor the process list. Only git gets it.
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + d.Token))
		gitEnv = append(gitEnv, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic "+auth)
	}
	git := func(ctx context.Context, out io.Writer, args ...string) (string, error) {
		stdout, err := runAsUser(ctx, out, s, s.Dir(), gitEnv, "git", args...)
		if err != nil {
			return stdout, fmt.Errorf("git %s: %w", args[0], err)
		}
		return stdout, nil
	}
	steps := []step{
		{title: "check the site", fn: func(ctx context.Context, out io.Writer) error {
			if _, err := lookupUser(s.SystemUser); err != nil {
				return fmt.Errorf("user %s does not exist; is the site set up?", s.SystemUser)
			}
			if info, err := os.Stat(rooted(s.Dir())); err != nil || !info.IsDir() {
				return fmt.Errorf("%s does not exist; is the site set up?", s.Dir())
			}
			if _, err := exec.LookPath("git"); err != nil {
				fmt.Fprintln(out, "installing git")
				install := aptInstall("git")
				return runCommand(ctx, out, install.args[0], install.args[1:]...)
			}
			return nil
		}},
	}
	if protocol.RepoUsesSSH(d.RepoURL) {
		steps = append(steps, step{title: "prepare SSH for GitHub", fn: func(ctx context.Context, out io.Writer) error {
			if _, err := os.Stat(rooted(deployKeyPath(s))); err != nil {
				return errors.New("the site has no deploy key yet; save the repository settings to create one")
			}
			return writeOwnedFile(s, knownHostsPath(s), []byte(githubKnownHosts), 0o644)
		}})
	}
	steps = append(steps,
		step{title: "get " + d.Branch + " from " + d.RepoURL, fn: func(ctx context.Context, out io.Writer) error {
			if _, err := os.Stat(rooted(s.Dir() + "/.git")); errors.Is(err, fs.ErrNotExist) {
				if _, err := git(ctx, out, "init", "--quiet"); err != nil {
					return err
				}
				if _, err := git(ctx, out, "remote", "add", "origin", d.RepoURL); err != nil {
					return err
				}
			} else if _, err := git(ctx, out, "remote", "set-url", "origin", d.RepoURL); err != nil {
				return err
			}
			if _, err := git(ctx, out, "fetch", "--prune", "origin", "+refs/heads/"+d.Branch+":refs/remotes/origin/"+d.Branch); err != nil {
				return err
			}
			// The placeholder would otherwise shadow a repository without index.php.
			index := s.Root() + "/index.php"
			if b, err := os.ReadFile(rooted(index)); err == nil && bytes.HasPrefix(b, []byte(keelPlaceholder)) {
				if err := os.Remove(rooted(index)); err != nil {
					return err
				}
				fmt.Fprintln(out, "removed Keel's placeholder index.php")
			}
			if _, err := git(ctx, out, "checkout", "--quiet", "--force", "-B", d.Branch, "refs/remotes/origin/"+d.Branch); err != nil {
				return err
			}
			head, err := git(ctx, io.Discard, "log", "-1", "--format=%H%n%s")
			if err != nil {
				return err
			}
			commit, message, _ := strings.Cut(strings.TrimSpace(head), "\n")
			result = protocol.DeployResult{Commit: commit, Message: message}
			fmt.Fprintf(out, "checked out %s: %s\n", shortSHA(commit), message)
			return nil
		}},
		step{title: "run the deploy script", fn: func(ctx context.Context, out io.Writer) error {
			if strings.TrimSpace(d.Script) == "" {
				fmt.Fprintln(out, "the deploy script is empty")
				return nil
			}
			if err := ensurePHPShim(s.PHPVersion); err != nil {
				return err
			}
			if err := ensureOwnedDir(s, s.HomeDir()+"/.keel", 0o700); err != nil {
				return err
			}
			script := s.HomeDir() + "/.keel/deploy.sh"
			if err := writeOwnedFile(s, script, []byte(d.Script), 0o700); err != nil {
				return err
			}
			env := userEnv(s, "KEEL_BRANCH="+d.Branch, "KEEL_COMMIT="+result.Commit)
			if _, err := runAsUser(ctx, out, s, s.Dir(), env, "bash", "-e", script); err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					return fmt.Errorf("the deploy script failed with status %d", exit.ExitCode())
				}
				return err
			}
			return nil
		}},
	)
	res := runSteps(ctx, send, id, steps)
	if result.Commit != "" {
		res.Result, _ = json.Marshal(result)
	}
	return res
}

// ensurePHPShim makes /usr/local/lib/keel/phpX.Y/php run that PHP version.
func ensurePHPShim(version string) error {
	dir := rooted(phpShimDir(version))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	link := dir + "/php"
	if dest, err := os.Readlink(link); err == nil && dest == "/usr/bin/php"+version {
		return nil
	}
	removeIfExists(link)
	return os.Symlink("/usr/bin/php"+version, link)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
