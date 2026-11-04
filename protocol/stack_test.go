package protocol

import (
	"slices"
	"testing"
)

func TestStackInstallValidate(t *testing.T) {
	valid := []StackInstall{
		{Component: ComponentNginx},
		{Component: ComponentPHP, Version: "7.4"},
		{Component: ComponentPHP, Version: "8.5"},
		{Component: ComponentMySQL},
		{Component: ComponentMariaDB},
		{Component: ComponentRedis},
		{Component: ComponentNodeJS, Version: "22"},
		{Component: ComponentCertbot},
	}
	for _, p := range valid {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	invalid := []StackInstall{
		{},
		{Component: "apache2"},
		{Component: "NGINX"},
		{Component: ComponentNginx, Version: "1.24"},
		{Component: ComponentPHP},
		{Component: ComponentPHP, Version: "8.6"},
		{Component: ComponentPHP, Version: "8.3; rm -rf /"},
		{Component: ComponentNodeJS},
		{Component: ComponentNodeJS, Version: "18"},
		{Component: ComponentNodeJS, Version: "22.1"},
		{Component: ComponentRedis, Version: "7"},
	}
	for _, p := range invalid {
		if err := p.Validate(); err == nil {
			t.Errorf("%+v: no error", p)
		}
	}
}

func TestInventoryClean(t *testing.T) {
	got := Inventory{
		Nginx:   "1.24.0",
		PHP:     []string{"8.3", "7.4", "8.3", "8.3; reboot", "10.0", ""},
		MySQL:   "<script>",
		NodeJS:  "22.11.0",
		Certbot: "",
	}.Clean()
	if got.Nginx != "1.24.0" || got.MySQL != "?" || got.NodeJS != "22.11.0" || got.Certbot != "" || got.Redis != "" {
		t.Fatalf("got %+v", got)
	}
	if !slices.Equal(got.PHP, []string{"7.4", "8.3"}) {
		t.Fatalf("php = %v", got.PHP)
	}
	if empty := (Inventory{}).Clean(); empty.PHP == nil {
		t.Fatal("PHP should be an empty list, not null")
	}
}

func TestNodeMajor(t *testing.T) {
	for in, want := range map[string]string{"22.11.0": "22", "8.0.1": "8", "": "", "abc": ""} {
		if got := NodeMajor(in); got != want {
			t.Errorf("NodeMajor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStackConflict(t *testing.T) {
	cases := []struct {
		p        StackInstall
		inv      Inventory
		conflict bool
	}{
		{StackInstall{Component: ComponentMySQL}, Inventory{}, false},
		{StackInstall{Component: ComponentMySQL}, Inventory{MySQL: "8.0.40"}, false},
		{StackInstall{Component: ComponentMySQL}, Inventory{MariaDB: "10.11.8"}, true},
		{StackInstall{Component: ComponentMariaDB}, Inventory{MySQL: "8.0.40"}, true},
		{StackInstall{Component: ComponentNodeJS, Version: "22"}, Inventory{}, false},
		{StackInstall{Component: ComponentNodeJS, Version: "22"}, Inventory{NodeJS: "22.11.0"}, false},
		{StackInstall{Component: ComponentNodeJS, Version: "24"}, Inventory{NodeJS: "22.11.0"}, true},
		{StackInstall{Component: ComponentPHP, Version: "8.3"}, Inventory{PHP: []string{"8.1"}}, false},
	}
	for _, c := range cases {
		if got := StackConflict(c.p, c.inv) != ""; got != c.conflict {
			t.Errorf("%+v on %+v: conflict = %v", c.p, c.inv, got)
		}
	}
}
