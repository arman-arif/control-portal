package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/protocol"
)

// useRealCommandsAsMe runs the site user's commands as the test's own user,
// with paths under /home moved into the fake root.
func useRealCommandsAsMe(t *testing.T, f *fakeSystem) {
	old := runAsUser
	t.Cleanup(func() { runAsUser = old })
	runAsUser = func(ctx context.Context, out io.Writer, s protocol.Site, dir string, env []string, name string, args ...string) (string, error) {
		for i, a := range args {
			if strings.HasPrefix(a, "/home/") {
				args[i] = f.root + a
			}
		}
		env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "PATH="+os.Getenv("PATH"))
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir, cmd.Env = f.root+dir, env
		var stdout strings.Builder
		cmd.Stdout, cmd.Stderr = io.MultiWriter(out, &stdout), out
		err := cmd.Run()
		return stdout.String(), err
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// testRepo makes a repository with one commit on main and returns its path
// and a function that commits a change to a file.
func testRepo(t *testing.T) (string, func(path, content, message string)) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
	commit := func(path, content, message string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755)
		os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644)
		gitIn(t, dir, "add", "-A")
		gitIn(t, dir, "commit", "--quiet", "-m", message)
	}
	commit("public/app.php", "<?php echo 'v1';", "first version")
	return dir, commit
}

func deployResult(t *testing.T, res protocol.JobResult) protocol.DeployResult {
	t.Helper()
	var r protocol.DeployResult
	json.Unmarshal(res.Result, &r)
	return r
}

func TestDeploy(t *testing.T) {
	f := newFakeSystem(t)
	useRealCommandsAsMe(t, f)
	s := testSite()
	if res := f.run(siteApplySteps(s, stackWithPHP)); !res.OK {
		t.Fatalf("site: %+v", res)
	}
	repo, commit := testRepo(t)
	os.WriteFile(f.root+s.Dir()+"/.env", []byte("APP_KEY=secret\n"), 0o640)

	d := protocol.Deploy{Site: s, RepoURL: repo, Branch: "main", TimeoutMinutes: 5,
		Script: "echo \"deployed $KEEL_BRANCH $KEEL_COMMIT\" > deployed.txt\n"}
	var logs logCapture
	res := runDeploy(context.Background(), logs.send, "j1", d)
	if !res.OK {
		t.Fatalf("first deploy: %+v\n%s", res, logs.text.String())
	}
	first := deployResult(t, res)
	if len(first.Commit) != 40 || first.Message != "first version" {
		t.Fatalf("result = %+v", first)
	}
	if b, _ := os.ReadFile(f.root + s.Dir() + "/deployed.txt"); string(b) != "deployed main "+first.Commit+"\n" {
		t.Fatalf("deployed.txt = %q", b)
	}
	if f.exists(s.Root() + "/index.php") {
		t.Fatal("Keel's placeholder is still there")
	}
	if b, _ := os.ReadFile(f.root + s.Dir() + "/.env"); string(b) != "APP_KEY=secret\n" {
		t.Fatal("the deploy touched .env")
	}
	if dest, err := os.Readlink(f.root + "/usr/local/lib/keel/php8.3/php"); err != nil || dest != "/usr/bin/php8.3" {
		t.Fatalf("php shim -> %q, %v", dest, err)
	}

	// A later deploy moves to the new commit and drops local edits.
	os.WriteFile(f.root+s.Root()+"/app.php", []byte("edited on the server"), 0o644)
	commit("public/app.php", "<?php echo 'v2';", "second version")
	res = runDeploy(context.Background(), logs.send, "j2", d)
	second := deployResult(t, res)
	if !res.OK || second.Commit == first.Commit || second.Message != "second version" {
		t.Fatalf("second deploy: %+v %+v", res, second)
	}
	if b, _ := os.ReadFile(f.root + s.Root() + "/app.php"); string(b) != "<?php echo 'v2';" {
		t.Fatalf("app.php = %q", b)
	}

	// A failing script fails the deploy, after the checkout.
	d.Script = "echo before\nfalse\necho after\n"
	logs = logCapture{}
	res = runDeploy(context.Background(), logs.send, "j3", d)
	if res.OK || !strings.Contains(res.Error, "deploy script failed with status 1") || strings.Contains(logs.text.String(), "after") {
		t.Fatalf("failing script: %+v\n%s", res, logs.text.String())
	}
	if deployResult(t, res).Commit != second.Commit {
		t.Fatal("a failed deploy should still report the commit it checked out")
	}

	// A branch that doesn't exist fails cleanly.
	d.Branch, d.Script = "nope", ""
	if res := runDeploy(context.Background(), logs.send, "j4", d); res.OK || !strings.Contains(res.Error, "git fetch") {
		t.Fatalf("missing branch: %+v", res)
	}
}

func TestDeployNeedsKeyForSSH(t *testing.T) {
	f := newFakeSystem(t)
	useRealCommandsAsMe(t, f)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	d := protocol.Deploy{Site: s, RepoURL: "git@github.com:acme/shop.git", Branch: "main", TimeoutMinutes: 5}
	var logs logCapture
	if res := runDeploy(context.Background(), logs.send, "j1", d); res.OK || !strings.Contains(res.Error, "no deploy key") {
		t.Fatalf("res = %+v", res)
	}
}

func TestDeployKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	f := newFakeSystem(t)
	useRealCommandsAsMe(t, f)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	var logs logCapture
	res := runDeployKey(context.Background(), logs.send, "j1", s)
	var key protocol.DeployKeyResult
	json.Unmarshal(res.Result, &key)
	if !res.OK || !strings.HasPrefix(key.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(key.PublicKey, " keel-example.com") {
		t.Fatalf("res = %+v key %q\n%s", res, key.PublicKey, logs.text.String())
	}
	if info, err := os.Stat(f.root + "/home/k_example_com/.ssh"); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf(".ssh: %v %v", info, err)
	}
	// Asking again returns the same key.
	res = runDeployKey(context.Background(), logs.send, "j2", s)
	var again protocol.DeployKeyResult
	json.Unmarshal(res.Result, &again)
	if again.PublicKey != key.PublicKey {
		t.Fatal("a second request made a new key")
	}
}
