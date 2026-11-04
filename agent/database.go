package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/arman-arif/control-portal/protocol"
)

// runMySQL runs SQL as the database root user over the local socket, which
// Ubuntu's MySQL and MariaDB allow for the system root user. It returns what
// the statements print, tab separated. The SQL itself never goes to the job
// log, since it can hold a password.
var runMySQL = func(ctx context.Context, out io.Writer, sql string) (string, error) {
	cmd := exec.CommandContext(ctx, "mysql", "--protocol=socket", "--user=root", "--batch", "--skip-column-names")
	cmd.Env = commandEnv()
	cmd.Stdin = strings.NewReader(sql)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, out
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mysql: %w", err)
	}
	return stdout.String(), nil
}

func runDatabaseJob(ctx context.Context, send sendFunc, id string, job protocol.Job) protocol.JobResult {
	inv, err := collectInventory(ctx)
	if err != nil {
		return protocol.JobResult{Error: "checking installed packages: " + err.Error()}
	}
	steps, err := databaseSteps(job, inv)
	if err != nil {
		return protocol.JobResult{Error: err.Error()}
	}
	return runSteps(ctx, send, id, steps)
}

func databaseSteps(job protocol.Job, inv protocol.Inventory) ([]step, error) {
	if inv.MySQL == "" && inv.MariaDB == "" {
		return nil, errors.New("neither MySQL nor MariaDB is installed; install one from the server's Stack tab")
	}
	switch job.Action {
	case protocol.ActionDBCreate, protocol.ActionDBDrop:
		var p protocol.DBParams
		if err := json.Unmarshal(job.Params, &p); err != nil {
			return nil, errors.New("malformed params")
		}
		if err := protocol.ValidateDBName(p.Name); err != nil {
			return nil, err
		}
		if job.Action == protocol.ActionDBCreate {
			return []step{{title: "create database " + p.Name, fn: func(ctx context.Context, out io.Writer) error {
				_, err := runMySQL(ctx, out, "CREATE DATABASE IF NOT EXISTS `"+p.Name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;\n")
				return err
			}}}, nil
		}
		return []step{{title: "drop database " + p.Name, fn: func(ctx context.Context, out io.Writer) error { return dropDatabase(ctx, out, p.Name) }}}, nil
	case protocol.ActionDBUserCreate, protocol.ActionDBUserGrants, protocol.ActionDBUserDrop:
		var p protocol.DBUserParams
		if err := json.Unmarshal(job.Params, &p); err != nil {
			return nil, errors.New("malformed params")
		}
		if err := p.Validate(job.Action == protocol.ActionDBUserCreate); err != nil {
			return nil, err
		}
		account := "'" + p.Name + "'@'localhost'"
		var steps []step
		switch job.Action {
		case protocol.ActionDBUserDrop:
			return []step{{title: "drop user " + p.Name, fn: func(ctx context.Context, out io.Writer) error {
				_, err := runMySQL(ctx, out, "DROP USER IF EXISTS "+account+";\n")
				return err
			}}}, nil
		case protocol.ActionDBUserCreate:
			steps = append(steps, step{title: "create user " + p.Name + " and set its password", fn: func(ctx context.Context, out io.Writer) error {
				_, err := runMySQL(ctx, out, "CREATE USER IF NOT EXISTS "+account+" IDENTIFIED BY '"+p.Password+"';\n"+
					"ALTER USER "+account+" IDENTIFIED BY '"+p.Password+"';\n")
				return err
			}})
		}
		title := "give " + p.Name + " access to no databases"
		if len(p.Databases) > 0 {
			title = "give " + p.Name + " access to " + strings.Join(p.Databases, ", ")
		}
		return append(steps, step{title: title, fn: func(ctx context.Context, out io.Writer) error {
			return runMySQLStatements(ctx, out, grantStatements(account, p.Databases))
		}}), nil
	}
	return nil, errors.New("unsupported action " + job.Action)
}

func runMySQLStatements(ctx context.Context, out io.Writer, statements []string) error {
	_, err := runMySQL(ctx, out, strings.Join(statements, "\n")+"\n")
	return err
}

// grantStatements makes databases the user's whole set of privileges.
func grantStatements(account string, databases []string) []string {
	stmts := []string{"REVOKE ALL PRIVILEGES, GRANT OPTION FROM " + account + ";"}
	for _, d := range databases {
		stmts = append(stmts, "GRANT ALL PRIVILEGES ON `"+protocol.GrantPattern(d)+"`.* TO "+account+";")
	}
	return stmts
}

var (
	grantUserPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)
	grantHostPattern = regexp.MustCompile(`^[A-Za-z0-9.%_:-]{1,255}$`)
)

// dropDatabase revokes every grant on the database, so a user can't regain
// access to a new database that later takes the name, then drops it.
func dropDatabase(ctx context.Context, out io.Writer, name string) error {
	pattern := protocol.GrantPattern(name)
	rows, err := runMySQL(ctx, out, "SELECT User, Host, Db FROM mysql.db WHERE Db IN ('"+name+"', '"+strings.ReplaceAll(pattern, `\`, `\\`)+"');\n")
	if err != nil {
		return err
	}
	var revokes []string
	for _, line := range strings.Split(strings.TrimSpace(rows), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		user, host, db := f[0], f[1], f[2]
		// mysql --batch escapes a backslash in its output.
		db = strings.ReplaceAll(db, `\\`, `\`)
		if !grantUserPattern.MatchString(user) || !grantHostPattern.MatchString(host) || (db != name && db != pattern) {
			return fmt.Errorf("unexpected grant on %s: %q", name, line)
		}
		revokes = append(revokes, "REVOKE ALL PRIVILEGES ON `"+db+"`.* FROM '"+user+"'@'"+host+"';")
		fmt.Fprintf(out, "revoking %s@%s's access\n", user, host)
	}
	return runMySQLStatements(ctx, out, append(revokes, "DROP DATABASE IF EXISTS `"+name+"`;"))
}
