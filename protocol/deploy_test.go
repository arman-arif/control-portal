package protocol

import (
	"strings"
	"testing"
)

func TestNormalizeRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/shop.git":      "git@github.com:acme/shop.git",
		"git@github.com:acme/shop":          "git@github.com:acme/shop.git",
		"acme/shop":                         "git@github.com:acme/shop.git",
		" acme/my_shop.v2 ":                 "git@github.com:acme/my_shop.v2.git",
		"https://github.com/acme/shop":      "https://github.com/acme/shop.git",
		"https://github.com/acme/shop.git/": "https://github.com/acme/shop.git",
	} {
		got, err := NormalizeRepoURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeRepoURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "shop", "acme/shop/extra", "https://gitlab.com/acme/shop", "git@gitlab.com:acme/shop.git",
		"http://github.com/acme/shop", "ssh://git@github.com/acme/shop", "file:///etc/passwd", "/srv/repo",
		"acme/../shop", "acme/-shop", "acme/sh op", "acme/shop;reboot", "git@github.com:acme/shop.git --upload-pack=x",
	} {
		if got, err := NormalizeRepoURL(in); err == nil {
			t.Errorf("NormalizeRepoURL(%q) = %q, want an error", in, got)
		}
	}
	if !RepoUsesSSH("git@github.com:a/b.git") || RepoUsesSSH("https://github.com/a/b.git") {
		t.Fatal("RepoUsesSSH")
	}
}

func TestValidateBranch(t *testing.T) {
	for _, b := range []string{"main", "release/2.x", "feature-x", "v1.2"} {
		if err := ValidateBranch(b); err != nil {
			t.Errorf("%q: %v", b, err)
		}
	}
	for _, b := range []string{"", "-f", "/main", "main/", "a..b", "a//b", "x.lock", ".hidden", "a/.b", "has space", "a;b", "a~1", "a^", "a:b"} {
		if ValidateBranch(b) == nil {
			t.Errorf("%q: no error", b)
		}
	}
}

func TestDeployValidate(t *testing.T) {
	d := Deploy{
		Site:    Site{Domain: "example.com", SystemUser: "k_example_com", PHPVersion: "8.3", WebDir: "public"},
		RepoURL: "git@github.com:acme/shop.git", Branch: "main", Script: DefaultDeployScriptV1, TimeoutMinutes: 60,
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	withToken := d
	withToken.RepoURL, withToken.Token = "https://github.com/acme/shop.git", "ghs_16C7e42F292c6912E7710c838347Ae178B4a"
	if err := withToken.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Deploy){
		"raw url":   func(d *Deploy) { d.RepoURL = "acme/shop" },
		"branch":    func(d *Deploy) { d.Branch = "-x" },
		"script":    func(d *Deploy) { d.Script = strings.Repeat("x", MaxDeployScript+1) },
		"timeout 0": func(d *Deploy) { d.TimeoutMinutes = 0 },
		"long":      func(d *Deploy) { d.TimeoutMinutes = MaxDeployTimeout + 1 },
		"site":      func(d *Deploy) { d.Site.SystemUser = "root" },
		"ssh token": func(d *Deploy) { d.Token = "ghs_abc" },
		"bad token": func(d *Deploy) { d.RepoURL, d.Token = "https://github.com/acme/shop.git", "ghs abc\n" },
	} {
		bad := d
		change(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
