package protocol

import (
	"strings"
	"testing"
)

func TestValidateDBName(t *testing.T) {
	for _, n := range []string{"app", "shop_db", "a", "wp2", strings.Repeat("a", 32)} {
		if err := ValidateDBName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	for _, n := range []string{"", "App", "1db", "_db", "my-db", "my db", "db;", "db`", "db'", "mysql", "root", "information_schema", strings.Repeat("a", 33)} {
		if err := ValidateDBName(n); err == nil {
			t.Errorf("%q: no error", n)
		}
	}
}

func TestValidateDBPassword(t *testing.T) {
	if err := ValidateDBPassword("Abc123Abc123Abc123Abc123"); err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{"", "short", "with space 123456789", "quote'1234567890123", strings.Repeat("a", 65)} {
		if ValidateDBPassword(pw) == nil {
			t.Errorf("%q: no error", pw)
		}
	}
}

func TestDBUserParamsValidate(t *testing.T) {
	ok := DBUserParams{Name: "shop", Password: "Abc123Abc123Abc123Abc123", Databases: []string{"shop", "shop_logs"}}
	if err := ok.Validate(true); err != nil {
		t.Fatal(err)
	}
	if err := (DBUserParams{Name: "shop", Databases: []string{}}).Validate(false); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []DBUserParams{
		{Name: "shop", Databases: []string{}},
		{Name: "root", Password: ok.Password},
		{Name: "shop", Password: ok.Password, Databases: []string{"mysql"}},
		{Name: "shop", Password: ok.Password, Databases: []string{"x`; DROP"}},
	} {
		if bad.Validate(true) == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

func TestGrantPattern(t *testing.T) {
	if got := GrantPattern("my_shop_db"); got != `my\_shop\_db` {
		t.Fatalf("got %q", got)
	}
}
