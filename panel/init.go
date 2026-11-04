package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/arman-arif/control-portal/panel/secrets"
)

const (
	secretKeyVar   = "KEEL_SECRET_KEY"
	defaultEnvFile = "/etc/keel/panel.env"
)

// runInit is the `keel-panel init` command.
func runInit(args []string) {
	flags := flag.NewFlagSet("init", flag.ExitOnError)
	envFile := flags.String("env-file", defaultEnvFile, "file to write "+secretKeyVar+" to")
	flags.Parse(args)
	if flags.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "keel-panel init takes no arguments, only --env-file PATH")
		os.Exit(2)
	}

	created, err := ensureSecretKey(*envFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "keel-panel init: %v\n", err)
		if errors.Is(err, os.ErrPermission) {
			fmt.Fprintln(os.Stderr, "Run it as root, or choose another file with --env-file PATH.")
		}
		os.Exit(1)
	}
	if created {
		fmt.Printf("Wrote a new %s to %s\n", secretKeyVar, *envFile)
		fmt.Println("Back this key up. Secrets encrypted with it cannot be recovered without it.")
	} else {
		fmt.Printf("%s already has a %s; left it unchanged.\n", *envFile, secretKeyVar)
		if info, err := os.Stat(*envFile); err == nil && info.Mode().Perm()&0o077 != 0 {
			fmt.Printf("Warning: other users can read this file (mode %04o). Consider: chmod 600 %s\n", info.Mode().Perm(), *envFile)
		}
	}
	// Absolute, because systemd needs that and `. file` searches PATH for a
	// bare file name.
	abs, err := filepath.Abs(*envFile)
	if err != nil {
		abs = *envFile
	}
	fmt.Printf("\nLoad it before starting the panel:\n")
	fmt.Printf("  in a shell:  set -a; . %s; set +a\n", abs)
	fmt.Printf("  in systemd:  EnvironmentFile=%s\n", abs)
}

// ensureSecretKey adds a new key to the env file, creating the file if needed.
// It never replaces a key the file already has, and reports whether it wrote
// one. Any other lines in the file are kept.
func ensureSecretKey(path string) (created bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if value, found := findEnvValue(existing, secretKeyVar); found {
		if _, err := secrets.New(value); err != nil {
			return false, fmt.Errorf("%s has a %s that is not usable (%v); fix or remove it by hand, since data may be encrypted with it", path, secretKeyVar, err)
		}
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// The file may have existed with a looser mode.
	if err := f.Chmod(0o600); err != nil {
		return false, err
	}
	line := secretKeyVar + "=" + secrets.GenerateKey() + "\n"
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		line = "\n" + line
	}
	if _, err := f.WriteString(line); err != nil {
		return false, err
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	return true, f.Close()
}

// findEnvValue returns the value the env file gives name. As in a shell, the
// last assignment wins.
func findEnvValue(content []byte, name string) (value string, found bool) {
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		rest, ok := strings.CutPrefix(line, name+"=")
		if !ok {
			continue
		}
		value, found = rest, true
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
	}
	return value, found
}
