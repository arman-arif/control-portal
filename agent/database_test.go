package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/arman-arif/control-portal/protocol"
)

var withMySQL = protocol.Inventory{MySQL: "8.0.46"}

// fakeMySQL records the SQL sent to mysql and answers queries with rows.
func fakeMySQL(t *testing.T, rows string) *[]string {
	var sqls []string
	old := runMySQL
	t.Cleanup(func() { runMySQL = old })
	runMySQL = func(ctx context.Context, out io.Writer, sql string) (string, error) {
		sqls = append(sqls, sql)
		if strings.HasPrefix(sql, "SELECT") {
			return rows, nil
		}
		return "", nil
	}
	return &sqls
}

func runDB(t *testing.T, action string, params any, inv protocol.Inventory) (protocol.JobResult, string) {
	t.Helper()
	raw, _ := json.Marshal(params)
	steps, err := databaseSteps(protocol.Job{Action: action, Params: raw}, inv)
	if err != nil {
		return protocol.JobResult{Error: err.Error()}, ""
	}
	var logs logCapture
	res := runSteps(context.Background(), logs.send, "j1", steps)
	return res, logs.text.String()
}

func TestDBCreate(t *testing.T) {
	sqls := fakeMySQL(t, "")
	if res, _ := runDB(t, protocol.ActionDBCreate, protocol.DBParams{Name: "shop_db"}, withMySQL); !res.OK {
		t.Fatalf("%+v", res)
	}
	if want := "CREATE DATABASE IF NOT EXISTS `shop_db` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n"; (*sqls)[0] != want {
		t.Fatalf("sql = %q", (*sqls)[0])
	}
}

func TestDBUserCreateKeepsPasswordOutOfLog(t *testing.T) {
	sqls := fakeMySQL(t, "")
	const pw = "Abc123Abc123Abc123Abc123"
	res, log := runDB(t, protocol.ActionDBUserCreate, protocol.DBUserParams{Name: "shop", Password: pw, Databases: []string{"shop_db", "logs"}}, withMySQL)
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	all := strings.Join(*sqls, "")
	for _, want := range []string{
		"CREATE USER IF NOT EXISTS 'shop'@'localhost' IDENTIFIED BY '" + pw + "';",
		"ALTER USER 'shop'@'localhost' IDENTIFIED BY '" + pw + "';",
		"REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'shop'@'localhost';",
		"GRANT ALL PRIVILEGES ON `shop\\_db`.* TO 'shop'@'localhost';",
		"GRANT ALL PRIVILEGES ON `logs`.* TO 'shop'@'localhost';",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("SQL misses %q:\n%s", want, all)
		}
	}
	if strings.Contains(log, pw) {
		t.Fatalf("the job log shows the password: %q", log)
	}
}

func TestDBUserGrantsOnlyRevokes(t *testing.T) {
	sqls := fakeMySQL(t, "")
	res, _ := runDB(t, protocol.ActionDBUserGrants, protocol.DBUserParams{Name: "shop", Databases: []string{}}, withMySQL)
	if !res.OK || len(*sqls) != 1 || strings.TrimSpace((*sqls)[0]) != "REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'shop'@'localhost';" {
		t.Fatalf("res %+v sql %q", res, *sqls)
	}
}

func TestDBDropRevokesGrantsFirst(t *testing.T) {
	sqls := fakeMySQL(t, "shop\tlocalhost\tshop\\\\_db\nold\t%\tshop_db\n")
	if res, _ := runDB(t, protocol.ActionDBDrop, protocol.DBParams{Name: "shop_db"}, protocol.Inventory{MariaDB: "10.11.8"}); !res.OK {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains((*sqls)[0], `WHERE Db IN ('shop_db', 'shop\\_db')`) {
		t.Fatalf("query = %q", (*sqls)[0])
	}
	want := "REVOKE ALL PRIVILEGES ON `shop\\_db`.* FROM 'shop'@'localhost';\n" +
		"REVOKE ALL PRIVILEGES ON `shop_db`.* FROM 'old'@'%';\n" +
		"DROP DATABASE IF EXISTS `shop_db`;\n"
	if (*sqls)[1] != want {
		t.Fatalf("got\n%s\nwant\n%s", (*sqls)[1], want)
	}
}

func TestDBDropRefusesOddGrants(t *testing.T) {
	sqls := fakeMySQL(t, "x'; DROP USER root; --\tlocalhost\tshop_db\n")
	if res, _ := runDB(t, protocol.ActionDBDrop, protocol.DBParams{Name: "shop_db"}, withMySQL); res.OK {
		t.Fatal("accepted a grant row with a strange user name")
	}
	if len(*sqls) != 1 {
		t.Fatalf("ran more SQL: %q", *sqls)
	}
}

func TestDBJobsCheckInput(t *testing.T) {
	sqls := fakeMySQL(t, "")
	cases := []struct {
		action string
		params any
		inv    protocol.Inventory
	}{
		{protocol.ActionDBCreate, protocol.DBParams{Name: "shop"}, protocol.Inventory{}},
		{protocol.ActionDBCreate, protocol.DBParams{Name: "x`; DROP DATABASE mysql; --"}, withMySQL},
		{protocol.ActionDBDrop, protocol.DBParams{Name: "mysql"}, withMySQL},
		{protocol.ActionDBUserCreate, protocol.DBUserParams{Name: "shop", Password: "short", Databases: []string{}}, withMySQL},
		{protocol.ActionDBUserCreate, protocol.DBUserParams{Name: "shop", Password: "pw' OR '1'='1aaaaaaaa", Databases: []string{}}, withMySQL},
		{protocol.ActionDBUserGrants, protocol.DBUserParams{Name: "shop", Databases: []string{"mysql"}}, withMySQL},
		{"db.exec", protocol.DBParams{Name: "shop"}, withMySQL},
	}
	for _, c := range cases {
		if res, _ := runDB(t, c.action, c.params, c.inv); res.OK {
			t.Errorf("%s %+v accepted", c.action, c.params)
		}
	}
	if len(*sqls) != 0 {
		t.Fatalf("ran SQL: %q", *sqls)
	}
}
