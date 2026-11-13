package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mainConfig = `
version: 1
state_dir: %STATE%
work_dir: %WORK%
repositories:
  location1-restic:
    type: restic
    repository: /tmp/repo
    password_file: %PASSFILE%
notifiers:
  ntfy-location1:
    type: ntfy
    url: https://ntfy.example.org/backups
    on: [fail, error, recovered]
server:
  listen: 127.0.0.1:8484
  timezone: UTC
drills:
  documents:
    repository: location1-restic
    schedule: '0 3 * * 0'
    snapshot:
      select: latest
      tags: [documents]
      max_age: 26h
    restore:
      paths: [/srv/service2/media]
    checks:
      - type: files_exist
        paths: ['srv/service2/media/**/*.pdf']
        min_count: 10
      - type: size_trend
        max_drop_percent: 10
`

func writeConfig(t *testing.T, content string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	passfile := filepath.Join(dir, "secret.pass")
	if err := os.WriteFile(passfile, []byte("canary-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content = strings.ReplaceAll(content, "%STATE%", filepath.Join(dir, "state"))
	content = strings.ReplaceAll(content, "%WORK%", filepath.Join(dir, "work"))
	content = strings.ReplaceAll(content, "%PASSFILE%", passfile)
	path := filepath.Join(dir, "restoredrill.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := writeConfig(t, mainConfig)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("version = %d", cfg.Version)
	}
	d := cfg.Drills["documents"]
	if d.Repository != "location1-restic" {
		t.Fatalf("repository = %q", d.Repository)
	}
	if d.Timeout.D().Minutes() != 30 {
		t.Fatalf("default timeout = %v", d.Timeout)
	}
	if d.Snapshot.Select != "latest" {
		t.Fatalf("select = %q", d.Snapshot.Select)
	}
	if len(d.Checks) != 2 {
		t.Fatalf("checks = %d", len(d.Checks))
	}
	if d.Checks[0].Name != "files_exist[0]" {
		t.Fatalf("auto name = %q", d.Checks[0].Name)
	}
	// The secret must be known to the redactor.
	if got := cfg.Redactor.String("the password is canary-secret-value ok"); strings.Contains(got, "canary-secret-value") {
		t.Fatalf("secret not redacted: %q", got)
	}
}

func TestUnknownVariableIsError(t *testing.T) {
	_, err := writeConfig(t, strings.ReplaceAll(mainConfig, "paths: [/srv/service2/media]", "paths: ['${env.NOPE_UNSET}/media']"))
	if err == nil || !strings.Contains(err.Error(), "unknown variable") {
		t.Fatalf("expected unknown variable error, got %v", err)
	}
}

func TestRuntimeVariablesAllowed(t *testing.T) {
	cfgText := `
version: 1
repositories:
  r:
    type: restic
    repository: /tmp/repo
drills:
  d:
    repository: r
    restore:
      paths: [/srv/app]
    sandbox:
      services:
        app:
          image: busybox:1.36
          mounts:
            - source: ${restore}/srv/app
              target: /data
    checks:
      - type: files_exist
        paths: ['srv/app/**']
`
	cfg, err := writeConfig(t, cfgText)
	if err != nil {
		t.Fatalf("runtime vars should be allowed at load time: %v", err)
	}
	d := cfg.Drills["d"]
	d.SubstituteRuntime("/work/run1/restore", "run1")
	if got := d.Sandbox.Services["app"].Mounts[0].Source; got != "/work/run1/restore/srv/app" {
		t.Fatalf("substitution = %q", got)
	}
}

func TestInlineSecretRejected(t *testing.T) {
	_, err := writeConfig(t, strings.ReplaceAll(mainConfig, "password_file: %PASSFILE%", "password: hunter2secret"))
	if err == nil || !strings.Contains(err.Error(), "inline secrets") {
		t.Fatalf("expected inline secret error, got %v", err)
	}
	cfg, err := writeConfig(t, "allow_inline_secrets: true\n"+strings.ReplaceAll(mainConfig, "password_file: %PASSFILE%", "password: hunter2secret"))
	if err != nil {
		t.Fatalf("allow_inline_secrets should permit it: %v", err)
	}
	if !strings.Contains(cfg.Redactor.String("hunter2secret"), "REDACTED") {
		t.Fatal("inline secret not registered with redactor")
	}
}

func TestUnknownCheckFieldRejected(t *testing.T) {
	_, err := writeConfig(t, strings.ReplaceAll(mainConfig, "min_count: 10", "min_count: 10\n        bogus_field: 1"))
	if err == nil || !strings.Contains(err.Error(), "not valid for check type") {
		t.Fatalf("expected field error, got %v", err)
	}
}

func TestWrongExpectKeyRejected(t *testing.T) {
	cfgText := strings.ReplaceAll(mainConfig, `- type: size_trend
        max_drop_percent: 10`, `- type: sqlite_query
        path: srv/db.sqlite3
        query: SELECT 1
        expect: { status: 200 }`)
	_, err := writeConfig(t, cfgText)
	if err == nil || !strings.Contains(err.Error(), "expectation key") {
		t.Fatalf("expectation key error expected, got %v", err)
	}
}

func TestBadCronRejected(t *testing.T) {
	_, err := writeConfig(t, strings.ReplaceAll(mainConfig, "'0 3 * * 0'", "'not a cron'"))
	if err == nil || !strings.Contains(err.Error(), "cron") {
		t.Fatalf("cron error expected, got %v", err)
	}
}

func TestNonLoopbackWithoutAuthRejected(t *testing.T) {
	_, err := writeConfig(t, strings.ReplaceAll(mainConfig, "127.0.0.1:8484", "0.0.0.0:8484"))
	if err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("non-loopback error expected, got %v", err)
	}
}

func TestIncludes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "drills.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := "version: 1\ninclude:\n  - drills.d/*.yaml\nrepositories:\n  r:\n    type: restic\n    repository: /tmp/repo\n"
	if err := os.WriteFile(filepath.Join(dir, "restoredrill.yaml"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	drill := "drills:\n  extra:\n    repository: r\n    restore:\n      paths: [/srv/x]\n    checks:\n      - type: files_exist\n        paths: ['srv/x/**']\n"
	if err := os.WriteFile(filepath.Join(dir, "drills.d", "extra.yaml"), []byte(drill), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(dir, "restoredrill.yaml"))
	if err != nil {
		t.Fatalf("include load: %v", err)
	}
	if _, ok := cfg.Drills["extra"]; !ok {
		t.Fatal("included drill missing")
	}
}

func TestRecipeMergeAndParams(t *testing.T) {
	dir := t.TempDir()
	recipe := `recipe: testapp
version: 1
description: test
params:
  repository: { required: true }
  data_path: { required: true }
  image: { default: 'busybox:1.36' }
drill:
  repository: ${param.repository}
  restore:
    paths: ['${param.data_path}']
  sandbox:
    services:
      app:
        image: ${param.image}
        mounts:
          - source: ${restore}${param.data_path}
            target: /data
  checks:
    - type: files_exist
      paths: ['srv/**']
`
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe), 0o600); err != nil {
		t.Fatal(err)
	}
	main := `version: 1
repositories:
  r:
    type: restic
    repository: /tmp/repo
drills:
  d:
    uses: ./recipe.yaml
    with:
      repository: r
      data_path: /srv/app
    schedule: '0 5 * * 1'
    checks:
      - type: snapshot_age
        max_age: 48h
`
	path := filepath.Join(dir, "restoredrill.yaml")
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("recipe load: %v", err)
	}
	d := cfg.Drills["d"]
	if d.Repository != "r" {
		t.Fatalf("repository = %q", d.Repository)
	}
	if d.Restore.Paths[0] != "/srv/app" {
		t.Fatalf("paths = %v", d.Restore.Paths)
	}
	if d.Sandbox.Services["app"].Image != "busybox:1.36" {
		t.Fatalf("default param not applied: %q", d.Sandbox.Services["app"].Image)
	}
	// Drill-level checks replace recipe checks (lists replace).
	if len(d.Checks) != 1 || d.Checks[0].Type != "snapshot_age" {
		t.Fatalf("checks override failed: %+v", d.Checks)
	}
	if d.Schedule != "0 5 * * 1" {
		t.Fatalf("schedule = %q", d.Schedule)
	}
}

// TestRecipeNumericParams covers non-string parameters: a substituted scalar
// must decode into int/bool/duration fields, not stay a quoted string.
func TestRecipeNumericParams(t *testing.T) {
	dir := t.TempDir()
	recipe := `recipe: numapp
version: 1
description: numeric params
params:
  repository: { required: true }
  data_path: { required: true }
  min_count: { default: 1 }
  max_age: { default: 26h }
  catch_up: { default: true }
  tag: { default: v1 }
drill:
  repository: ${param.repository}
  catch_up: ${param.catch_up}
  snapshot:
    select: latest
    tags: ['${param.tag}']
  restore:
    paths: ['${param.data_path}']
  checks:
    - type: files_exist
      paths: ['srv/**']
      min_count: ${param.min_count}
    - type: snapshot_age
      max_age: ${param.max_age}
`
	if err := os.WriteFile(filepath.Join(dir, "recipe.yaml"), []byte(recipe), 0o600); err != nil {
		t.Fatal(err)
	}
	main := `version: 1
repositories:
  r:
    type: restic
    repository: /tmp/repo
drills:
  d:
    uses: ./recipe.yaml
    with:
      repository: r
      data_path: /srv/1234
      min_count: 5
      catch_up: false
      tag: 12345
  defaults:
    uses: ./recipe.yaml
    with:
      repository: r
      data_path: /srv/app
`
	path := filepath.Join(dir, "restoredrill.yaml")
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("recipe load: %v", err)
	}
	d := cfg.Drills["d"]
	if d.Checks[0].MinCount != 5 {
		t.Errorf("min_count = %d, want 5", d.Checks[0].MinCount)
	}
	if d.Checks[1].MaxAge.D() != 26*time.Hour {
		t.Errorf("max_age = %v, want 26h", d.Checks[1].MaxAge.D())
	}
	if d.CatchUp == nil || *d.CatchUp {
		t.Errorf("catch_up = %v, want false", d.CatchUp)
	}
	// A numeric-looking value in a string field stays a string.
	if d.Restore.Paths[0] != "/srv/1234" {
		t.Errorf("data_path = %q, want /srv/1234", d.Restore.Paths[0])
	}
	// A numeric param value substituted into a string field decodes as a string.
	if len(d.Snapshot.Tags) != 1 || d.Snapshot.Tags[0] != "12345" {
		t.Errorf("snapshot tags = %v, want [12345] as string", d.Snapshot.Tags)
	}
	// Defaults apply when the parameter is not given.
	dd := cfg.Drills["defaults"]
	if dd.Checks[0].MinCount != 1 {
		t.Errorf("default min_count = %d, want 1", dd.Checks[0].MinCount)
	}
	if dd.CatchUp == nil || !*dd.CatchUp {
		t.Errorf("default catch_up = %v, want true", dd.CatchUp)
	}
}

func TestRecipeMissingRequiredParam(t *testing.T) {
	dir := t.TempDir()
	recipe := "recipe: x\nversion: 1\nparams:\n  repository: { required: true }\ndrill:\n  repository: ${param.repository}\n"
	if err := os.WriteFile(filepath.Join(dir, "r.yaml"), []byte(recipe), 0o600); err != nil {
		t.Fatal(err)
	}
	main := "version: 1\ndrills:\n  d:\n    uses: ./r.yaml\n"
	path := filepath.Join(dir, "restoredrill.yaml")
	if err := os.WriteFile(path, []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "missing required parameter") {
		t.Fatalf("expected missing param error, got %v", err)
	}
}

func TestSecretFilePermissionsWarning(t *testing.T) {
	dir := t.TempDir()
	passfile := filepath.Join(dir, "secret.pass")
	if err := os.WriteFile(passfile, []byte("canary-secret-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := strings.ReplaceAll(mainConfig, "%STATE%", filepath.Join(dir, "state"))
	content = strings.ReplaceAll(content, "%WORK%", filepath.Join(dir, "work"))
	content = strings.ReplaceAll(content, "%PASSFILE%", passfile)
	path := filepath.Join(dir, "restoredrill.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "readable by group/others") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected permission warning, got %v", cfg.Warnings)
	}
}

func TestDurationParsing(t *testing.T) {
	cases := map[string]int64{
		"30m": 30 * 60, "24h": 24 * 3600, "30d": 30 * 86400, "2w": 14 * 86400,
		"90s": 90, "1h30m": 5400,
	}
	for in, wantSec := range cases {
		d, err := ParseDuration(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if int64(d.Seconds()) != wantSec {
			t.Fatalf("%s = %v, want %ds", in, d, wantSec)
		}
	}
	if _, err := ParseDuration("bogus"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{"2GiB": 2 << 30, "512MiB": 512 << 20, "1KB": 1000, "1024": 1024}
	for in, want := range cases {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Fatalf("%s = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestJSONSchemaGenerates(t *testing.T) {
	b, err := JSONSchemaBytes()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"repositories", "drills", "snapshot_age", "password_file"} {
		if !strings.Contains(s, want) {
			t.Fatalf("schema missing %q", want)
		}
	}
}