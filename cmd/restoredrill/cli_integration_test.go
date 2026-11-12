//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "rd-cli-test")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(tmp, "restoredrill")
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		panic("build failed: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// runCLI runs the built binary in dir and returns stdout, exit code.
func runCLI(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return string(out), code
}

func setupCLIFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic not installed")
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "src", "srv", "data")
	os.MkdirAll(data, 0o755)
	for i := 0; i < 4; i++ {
		os.WriteFile(filepath.Join(data, string(rune('a'+i))+".txt"), []byte("hello"), 0o644)
	}
	repo := filepath.Join(dir, "repo")
	env := append(os.Environ(), "RESTIC_PASSWORD=cli-pass", "RESTIC_REPOSITORY="+repo)
	for _, args := range [][]string{{"init"}, {"backup", "--tag", "t", data}} {
		cmd := exec.Command("restic", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("restic %v: %v\n%s", args, err, out)
		}
	}
	pass := filepath.Join(dir, "pass")
	os.WriteFile(pass, []byte("cli-pass\n"), 0o600)
	cfg := "version: 1\nstate_dir: " + filepath.Join(dir, "state") + "\nwork_dir: " + filepath.Join(dir, "work") +
		"\nrepositories:\n  r:\n    type: restic\n    repository: " + repo + "\n    password_file: " + pass +
		"\ndrills:\n  d:\n    repository: r\n    snapshot: {select: latest, tags: [t]}\n    restore:\n      paths: [" + data + "]" +
		"\n    checks:\n      - type: files_exist\n        paths: ['**/*.txt']\n        min_count: 4\n"
	os.WriteFile(filepath.Join(dir, "restoredrill.yaml"), []byte(cfg), 0o600)
	return dir
}

func TestCLIInitValidate(t *testing.T) {
	dir := t.TempDir()
	out, code := runCLI(t, dir, "init", "--dir", dir)
	if code != 0 {
		t.Fatalf("init rc=%d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "restoredrill.yaml")); err != nil {
		t.Fatal("init did not write config")
	}
	// The example config references a nonexistent repo path but is schema-valid.
	out, code = runCLI(t, dir, "validate", "--config", filepath.Join(dir, "restoredrill.yaml"))
	if code != 0 {
		t.Fatalf("validate rc=%d: %s", code, out)
	}
}

func TestCLIRunJSONAndExitCodes(t *testing.T) {
	dir := setupCLIFixture(t)
	out, code := runCLI(t, dir, "run", "d", "--json", "--log-level", "error")
	if code != 0 {
		t.Fatalf("run rc=%d: %s", code, out)
	}
	var env struct {
		Schema int `json:"schema"`
		Data   []struct {
			Status string `json:"status"`
			Drill  string `json:"drill"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if env.Schema != 1 || len(env.Data) != 1 || env.Data[0].Status != "pass" {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	// status --json
	out, code = runCLI(t, dir, "status", "--json")
	if code != 0 || !strings.Contains(out, `"schema": 1`) {
		t.Fatalf("status rc=%d: %s", code, out)
	}

	// Force a failure and check exit code 1.
	cfgPath := filepath.Join(dir, "restoredrill.yaml")
	b, _ := os.ReadFile(cfgPath)
	os.WriteFile(cfgPath, []byte(strings.ReplaceAll(string(b), "min_count: 4", "min_count: 999")), 0o600)
	_, code = runCLI(t, dir, "run", "d", "--log-level", "error")
	if code != 1 {
		t.Fatalf("expected exit 1 for failure, got %d", code)
	}
}

func TestCLIUsageErrorExitCode(t *testing.T) {
	dir := t.TempDir()
	_, code := runCLI(t, dir, "run", "nonexistent", "--config", filepath.Join(dir, "missing.yaml"))
	if code != 2 {
		t.Fatalf("expected exit 2 for config error, got %d", code)
	}
}