package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/protocol"
)

// envSetup runs the .env scripts for real, as the test's user, in a fake root.
func envSetup(t *testing.T) (*fakeSystem, protocol.Site) {
	f := newFakeSystem(t)
	s := testSite()
	f.run(siteApplySteps(s, stackWithPHP))
	old := runAsUserWithInput
	t.Cleanup(func() { runAsUserWithInput = old })
	runAsUserWithInput = func(ctx context.Context, s protocol.Site, dir, stdin, name string, args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = f.root + dir
		cmd.Stdin = strings.NewReader(stdin)
		var out, errOut strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		return out.String(), errOut.String(), err
	}
	return f, s
}

func envJob(t *testing.T, action string, s protocol.Site, content string) (protocol.JobResult, protocol.EnvFile) {
	t.Helper()
	params, _ := json.Marshal(protocol.EnvParams{Site: s, Content: content})
	res := runEnvJob(context.Background(), protocol.Job{Action: action, Params: params})
	var file protocol.EnvFile
	json.Unmarshal(res.Result, &file)
	return res, file
}

func TestEnvReadWrite(t *testing.T) {
	f, s := envSetup(t)
	res, file := envJob(t, protocol.ActionSiteEnvRead, s, "")
	if !res.OK || file.Exists || file.Content != "" {
		t.Fatalf("missing file: %+v %+v", res, file)
	}
	content := "APP_KEY=base64:abc\nDB_PASSWORD=\"p@ss word\"\n"
	if res, _ := envJob(t, protocol.ActionSiteEnvWrite, s, content); !res.OK {
		t.Fatalf("write: %+v", res)
	}
	info, err := os.Stat(f.root + s.Dir() + "/.env")
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode: %v %v", info, err)
	}
	if res, file := envJob(t, protocol.ActionSiteEnvRead, s, ""); !res.OK || !file.Exists || file.Content != content {
		t.Fatalf("read back: %+v %+v", res, file)
	}
	// No temporary files left behind.
	entries, _ := os.ReadDir(f.root + s.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".env.keel-") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

func TestEnvWriteReplacesSymlink(t *testing.T) {
	f, s := envSetup(t)
	target := f.root + "/etc-shadow-standin"
	os.WriteFile(target, []byte("root:secret\n"), 0o600)
	os.Symlink(target, f.root+s.Dir()+"/.env")
	if res, _ := envJob(t, protocol.ActionSiteEnvWrite, s, "A=1\n"); !res.OK {
		t.Fatalf("write: %+v", res)
	}
	if b, _ := os.ReadFile(target); string(b) != "root:secret\n" {
		t.Fatal("the write went through the symlink")
	}
	if info, err := os.Lstat(f.root + s.Dir() + "/.env"); err != nil || !info.Mode().IsRegular() {
		t.Fatalf(".env is not a regular file now: %v", err)
	}
}

func TestEnvRejects(t *testing.T) {
	f, s := envSetup(t)
	os.Mkdir(f.root+s.Dir()+"/.env", 0o755)
	if res, _ := envJob(t, protocol.ActionSiteEnvRead, s, ""); res.OK || !strings.Contains(res.Error, "not a regular file") {
		t.Fatalf("directory: %+v", res)
	}
	if res, _ := envJob(t, protocol.ActionSiteEnvWrite, s, strings.Repeat("x", protocol.MaxEnvFile+1)); res.OK {
		t.Fatal("accepted a file over 256 KB")
	}
	bad := s
	bad.SystemUser = "root"
	if res, _ := envJob(t, protocol.ActionSiteEnvRead, bad, ""); res.OK {
		t.Fatal("accepted a non-Keel user")
	}
}
