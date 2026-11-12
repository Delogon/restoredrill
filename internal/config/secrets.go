package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/restoredrill/restoredrill/internal/redact"
)

// resolveSecrets learns every configured secret value and registers it with
// the redactor. Secret files are read; *_command values are executed locally
// (they are local helper commands such as "cat /path/to/pass"). Failures are
// recorded as warnings: the adapter will surface the real error later, but
// the value cannot be redacted if it cannot be learned.
func resolveSecrets(c *Config) *redact.Redactor {
	r := redact.New()

	learnFile := func(path, what string) {
		if path == "" {
			return
		}
		b, err := os.ReadFile(path)
		if err != nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s: cannot read %q: %v — secret cannot be redacted from output", what, path, err))
			return
		}
		r.Add(string(b))
	}
	learnCommand := func(command, what string) {
		if command == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sh", "-c", command).Output()
		if err != nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s: command failed: %v — secret cannot be redacted from output", what, err))
			return
		}
		r.Add(string(out))
	}

	for name, repo := range c.Repositories {
		what := "repository " + name
		learnFile(repo.PasswordFile, what+" password_file")
		learnCommand(repo.PasswordCommand, what+" password_command")
		learnCommand(repo.Passcommand, what+" passcommand")
		r.Add(repo.Password, repo.Passphrase)
		for k, v := range repo.Env {
			if isSecretEnvKey(k) {
				r.Add(v)
			}
		}
	}
	for name, n := range c.Notifiers {
		what := "notifier " + name
		learnFile(n.TokenFile, what+" token_file")
		learnFile(n.SecretFile, what+" secret_file")
		r.Add(n.Token, n.Secret)
	}
	learnFile(c.Server.Auth.APITokenFile, "server api_token_file")

	// Secrets inherited from the process environment must never leak either.
	for _, k := range []string{"RESTIC_PASSWORD", "BORG_PASSPHRASE"} {
		if v := os.Getenv(k); v != "" {
			r.Add(v)
		}
	}
	return r
}

func isSecretEnvKey(k string) bool {
	u := strings.ToUpper(k)
	return strings.Contains(u, "PASSWORD") || strings.Contains(u, "PASSPHRASE") ||
		strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET")
}

// checkSecretFilePerms warns when a secret file is readable by group or
// others.
func checkSecretFilePerms(c *Config) {
	check := func(path, what string) {
		if path == "" {
			return
		}
		fi, err := os.Stat(path)
		if err != nil {
			return // missing files are reported elsewhere
		}
		if fi.Mode().Perm()&0o077 != 0 {
			c.Warnings = append(c.Warnings, fmt.Sprintf("%s: %q is readable by group/others (mode %04o) — run: chmod 600 %s", what, path, fi.Mode().Perm(), path))
		}
	}
	for name, repo := range c.Repositories {
		check(repo.PasswordFile, "repository "+name+" password_file")
	}
	for name, n := range c.Notifiers {
		check(n.TokenFile, "notifier "+name+" token_file")
		check(n.SecretFile, "notifier "+name+" secret_file")
	}
	check(c.Server.Auth.APITokenFile, "server api_token_file")
}