package protocol

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateDomain(t *testing.T) {
	for _, d := range []string{"example.com", "blog.example.co.uk", "a.io", "xn--bcher-kva.example", "my-site.dev", "1password.com"} {
		if err := ValidateDomain(d); err != nil {
			t.Errorf("%q: %v", d, err)
		}
	}
	for _, d := range []string{
		"", "localhost", "Example.com", "-a.com", "a-.com", "a..com", ".a.com", "a.com.",
		"*.example.com", "ex ample.com", "example.com;", "10.0.3.1", "a/b.com", "exämple.com",
		strings.Repeat("a", 64) + ".com", strings.Repeat("abcdefgh.", 32) + "com",
	} {
		if err := ValidateDomain(d); err == nil {
			t.Errorf("%q: no error", d)
		}
	}
}

func TestValidateWebDir(t *testing.T) {
	for _, d := range []string{"", "public", "web/dist", "public_html", "app.v2/public"} {
		if err := ValidateWebDir(d); err != nil {
			t.Errorf("%q: %v", d, err)
		}
	}
	for _, d := range []string{"/public", "public/", "..", "../etc", "a/../b", "./public", ".hidden", "a//b", "a b", "a;b", "pub\nlic"} {
		if err := ValidateWebDir(d); err == nil {
			t.Errorf("%q: no error", d)
		}
	}
}

func TestSiteValidate(t *testing.T) {
	good := Site{Domain: "example.com", Aliases: []string{"www.example.com"}, SystemUser: "k_example_com", WebDir: "public", PHPVersion: "8.3"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Site){
		"bad domain":      func(s *Site) { s.Domain = "example" },
		"bad alias":       func(s *Site) { s.Aliases = []string{"www"} },
		"alias is domain": func(s *Site) { s.Aliases = []string{"example.com"} },
		"duplicate alias": func(s *Site) { s.Aliases = []string{"a.example.com", "a.example.com"} },
		"too many":        func(s *Site) { s.Aliases = make([]string, MaxAliases+1) },
		"root user":       func(s *Site) { s.SystemUser = "root" },
		"other user":      func(s *Site) { s.SystemUser = "www-data" },
		"user path":       func(s *Site) { s.SystemUser = "k_../../etc" },
		"web dir":         func(s *Site) { s.WebDir = "../../etc" },
		"php":             func(s *Site) { s.PHPVersion = "5.6" },
	}
	for name, change := range bad {
		s := good
		s.Aliases = slices.Clone(good.Aliases)
		change(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestSitePaths(t *testing.T) {
	s := Site{Domain: "example.com", SystemUser: "k_example_com", WebDir: "public"}
	if s.Dir() != "/home/k_example_com/example.com" || s.Root() != "/home/k_example_com/example.com/public" {
		t.Fatalf("dir %q root %q", s.Dir(), s.Root())
	}
	s.WebDir = ""
	if s.Root() != s.Dir() {
		t.Fatalf("root without web dir = %q", s.Root())
	}
}

func TestSystemUserBase(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":                         "k_example_com",
		"my-site.co.uk":                       "k_my_site_co_uk",
		"a-very-long-domain-name.example.com": "k_a_very_long_domain_name",
		"abcdefghijklmnopqrstuvw.x.com":       "k_abcdefghijklmnopqrstuvw",
	} {
		got := SystemUserBase(in)
		if got != want {
			t.Errorf("SystemUserBase(%q) = %q, want %q", in, got, want)
		}
		if !systemUser.MatchString(got) || !systemUser.MatchString(got+"_20") {
			t.Errorf("%q (with a suffix) is not a valid system user", got)
		}
	}
}

func TestParseAliases(t *testing.T) {
	got := ParseAliases(" WWW.example.com,blog.example.com\nwww.example.com  shop.example.com, ")
	want := []string{"www.example.com", "blog.example.com", "shop.example.com"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	if got := ParseAliases(""); got == nil || len(got) != 0 {
		t.Fatalf("empty: %#v", got)
	}
}

func TestACMEValidate(t *testing.T) {
	for _, a := range []ACME{
		{Email: "ops@example.com"},
		{Email: "a.b+keel@sub.example.co.uk", Server: "https://acme-staging-v02.api.letsencrypt.org/directory"},
		{Email: "ops@example.com", Server: "https://10.0.3.50:14000/dir"},
	} {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	for _, a := range []ACME{
		{},
		{Email: "not an email"},
		{Email: "Ops <ops@example.com>"},
		{Email: "ops@example.com; reboot"},
		{Email: "ops@example.com", Server: "http://acme.example.com/dir"},
		{Email: "ops@example.com", Server: "https://acme.example.com/dir --staging"},
		{Email: "ops@example.com", Server: "file:///etc/passwd"},
	} {
		if err := a.Validate(); err == nil {
			t.Errorf("%+v: no error", a)
		}
	}
}

func TestSSLSiteNeedsACME(t *testing.T) {
	s := Site{Domain: "example.com", SystemUser: "k_example_com", PHPVersion: "8.3", SSL: true}
	if s.Validate() == nil {
		t.Fatal("SSL site without ACME settings accepted")
	}
	s.ACME = &ACME{Email: "ops@example.com"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.CertName() != "keel-example.com" {
		t.Fatalf("cert name %q", s.CertName())
	}
}

func TestInventoryCleanCertificates(t *testing.T) {
	inv := Inventory{Certificates: []Certificate{
		{Domain: "example.com", Names: []string{"example.com", "www.example.com", "bad name"}},
		{Domain: "../../etc", Names: []string{"x.com"}},
	}}.Clean()
	if len(inv.Certificates) != 1 || len(inv.Certificates[0].Names) != 2 {
		t.Fatalf("certificates = %+v", inv.Certificates)
	}
	if (Inventory{}).Clean().Certificates == nil {
		t.Fatal("certificates should be an empty list, not null")
	}
}
