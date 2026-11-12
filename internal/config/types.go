// Package config loads, merges, substitutes and validates restoredrill
// configuration. Configuration is declarative YAML; it is never edited by the
// web UI so it can live in Git.
package config

import (
	"github.com/restoredrill/restoredrill/internal/redact"
)

// SchemaVersion is the only supported config version.
const SchemaVersion = 1

// Config is the main restoredrill.yaml file.
type Config struct {
	Version          int                   `yaml:"version" json:"version"`
	StateDir         string                `yaml:"state_dir" json:"state_dir"`
	WorkDir          string                `yaml:"work_dir" json:"work_dir"`
	Binaries         Binaries              `yaml:"binaries" json:"binaries"`
	Retention        Retention             `yaml:"retention" json:"retention"`
	Defaults         Defaults              `yaml:"defaults" json:"defaults"`
	Repositories     map[string]Repository `yaml:"repositories" json:"repositories"`
	Notifiers        map[string]Notifier   `yaml:"notifiers" json:"notifiers"`
	Server           Server                `yaml:"server" json:"server"`
	Include          []string              `yaml:"include" json:"include,omitempty"`
	Drills           map[string]Drill      `yaml:"drills" json:"drills"`
	AllowInlineSecrets bool                `yaml:"allow_inline_secrets" json:"allow_inline_secrets,omitempty"`

	// Path is the file the config was loaded from (not serialized).
	Path string `yaml:"-" json:"-"`
	// Dir is the directory containing the main config file (not serialized).
	Dir string `yaml:"-" json:"-"`
	// Redactor knows every configured secret value (not serialized).
	Redactor *redact.Redactor `yaml:"-" json:"-"`
	// Warnings collected during load/validation (not serialized).
	Warnings []string `yaml:"-" json:"-"`
}

// Binaries locates the external backup tools.
type Binaries struct {
	Restic string `yaml:"restic" json:"restic"`
	Borg   string `yaml:"borg" json:"borg"`
}

// Retention controls how much run history is kept.
type Retention struct {
	RunsPerDrill int `yaml:"runs_per_drill" json:"runs_per_drill"`
}

// Defaults are applied to every drill that does not set the field itself.
type Defaults struct {
	Timeout       Duration   `yaml:"timeout" json:"timeout"`
	KeepOnFailure bool       `yaml:"keep_on_failure" json:"keep_on_failure"`
	KeepTTL       Duration   `yaml:"keep_ttl" json:"keep_ttl"`
	Notify        []string   `yaml:"notify" json:"notify"`
	Resources     *Resources `yaml:"resources" json:"resources,omitempty"`
}

// Resources are container resource limits.
type Resources struct {
	CPUs   float64 `yaml:"cpus" json:"cpus"`
	Memory string  `yaml:"memory" json:"memory"`
}

// MemoryBytes parses Memory, returning 0 when unset.
func (r *Resources) MemoryBytes() (int64, error) {
	if r == nil || r.Memory == "" {
		return 0, nil
	}
	return ParseBytes(r.Memory)
}

// Repository is a restic or Borg repository plus how to unlock it.
type Repository struct {
	Type       string            `yaml:"type" json:"type"`
	Repository string            `yaml:"repository" json:"repository"`
	NoLock     bool              `yaml:"no_lock" json:"no_lock,omitempty"`
	Env        map[string]string `yaml:"env" json:"env,omitempty"`

	// restic unlock material. At most one may be set.
	PasswordFile    string `yaml:"password_file" json:"password_file,omitempty"`
	PasswordCommand string `yaml:"password_command" json:"password_command,omitempty"`
	Password        string `yaml:"password" json:"password,omitempty"` // inline: rejected by default

	// Borg unlock material.
	Passcommand string `yaml:"passcommand" json:"passcommand,omitempty"`
	Passphrase  string `yaml:"passphrase" json:"passphrase,omitempty"` // inline: rejected by default
}

// Notifier sends run result notifications.
type Notifier struct {
	Type string   `yaml:"type" json:"type"`
	On   []string `yaml:"on" json:"on"`

	URL        string `yaml:"url" json:"url,omitempty"`               // ntfy, webhook
	Homeserver string `yaml:"homeserver" json:"homeserver,omitempty"` // matrix
	RoomID     string `yaml:"room_id" json:"room_id,omitempty"`       // matrix

	TokenFile string `yaml:"token_file" json:"token_file,omitempty"`
	Token     string `yaml:"token" json:"token,omitempty"` // inline: rejected by default

	SecretFile string `yaml:"secret_file" json:"secret_file,omitempty"` // webhook HMAC key
	Secret     string `yaml:"secret" json:"secret,omitempty"`           // inline: rejected by default

	RenotifyAfter Duration `yaml:"renotify_after" json:"renotify_after,omitempty"`
}

// Server configures the dashboard, API and scheduler process.
type Server struct {
	Listen    string `yaml:"listen" json:"listen"`
	PublicURL string `yaml:"public_url" json:"public_url,omitempty"`
	Auth      Auth   `yaml:"auth" json:"auth"`
	Timezone  string `yaml:"timezone" json:"timezone"`
}

// Auth configures dashboard authentication.
type Auth struct {
	Mode           string              `yaml:"mode" json:"mode"` // none | basic | trusted_header
	Users          map[string]AuthUser `yaml:"users" json:"users,omitempty"`
	TrustedProxies []string            `yaml:"trusted_proxies" json:"trusted_proxies,omitempty"`
	Header         string              `yaml:"header" json:"header,omitempty"`
	APITokenFile   string              `yaml:"api_token_file" json:"api_token_file,omitempty"`
}

// AuthUser is a basic-auth user with a bcrypt password hash.
type AuthUser struct {
	PasswordHash string `yaml:"password_hash" json:"password_hash"`
}

// Drill is one named restore test.
type Drill struct {
	Uses       string         `yaml:"uses,omitempty" json:"uses,omitempty"`
	With       map[string]any `yaml:"with,omitempty" json:"with,omitempty"`
	Repository string         `yaml:"repository" json:"repository"`
	Schedule   string         `yaml:"schedule,omitempty" json:"schedule,omitempty"`
	Jitter     Duration       `yaml:"jitter,omitempty" json:"jitter,omitempty"`
	CatchUp    *bool          `yaml:"catch_up,omitempty" json:"catch_up,omitempty"`
	Snapshot   Snapshot       `yaml:"snapshot" json:"snapshot"`
	Restore    Restore        `yaml:"restore" json:"restore"`
	Sandbox    *Sandbox       `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	Checks     []Check        `yaml:"checks" json:"checks"`
	Timeout    Duration       `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	KeepOnFailure *bool       `yaml:"keep_on_failure,omitempty" json:"keep_on_failure,omitempty"`
	KeepTTL    Duration       `yaml:"keep_ttl,omitempty" json:"keep_ttl,omitempty"`
	Notify     []string       `yaml:"notify,omitempty" json:"notify,omitempty"`
	Resources  *Resources     `yaml:"resources,omitempty" json:"resources,omitempty"`
}

// Snapshot selects which snapshot to test.
type Snapshot struct {
	Select      string   `yaml:"select" json:"select"` // latest | random | oldest
	Within      Duration `yaml:"within,omitempty" json:"within,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Host        string   `yaml:"host,omitempty" json:"host,omitempty"`
	Paths       []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	ArchiveGlob string   `yaml:"archive_glob,omitempty" json:"archive_glob,omitempty"`
	MaxAge      Duration `yaml:"max_age,omitempty" json:"max_age,omitempty"`
}

// Restore lists the absolute source paths to restore.
type Restore struct {
	Paths []string `yaml:"paths" json:"paths"`
}

// Sandbox configures optional service containers.
type Sandbox struct {
	Network  string             `yaml:"network,omitempty" json:"network,omitempty"` // isolated (default)
	Services map[string]Service `yaml:"services,omitempty" json:"services,omitempty"`
}

// Service is one container started from restored data.
type Service struct {
	Image       string            `yaml:"image" json:"image"`
	Mounts      []Mount           `yaml:"mounts,omitempty" json:"mounts,omitempty"`
	Env         map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	Healthcheck *Healthcheck      `yaml:"healthcheck,omitempty" json:"healthcheck,omitempty"`
	Resources   *Resources        `yaml:"resources,omitempty" json:"resources,omitempty"`
	CapAdd      []string          `yaml:"cap_add,omitempty" json:"cap_add,omitempty"`
	User        string            `yaml:"user,omitempty" json:"user,omitempty"`
}

// Mount binds a restored path into a service container.
type Mount struct {
	Source   string `yaml:"source" json:"source"`
	Target   string `yaml:"target" json:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty" json:"read_only,omitempty"`
}

// Healthcheck waits for a service to become ready.
type Healthcheck struct {
	HTTP    *HTTPProbe `yaml:"http,omitempty" json:"http,omitempty"`
	Timeout Duration   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// HTTPProbe describes an HTTP request and its expectation.
type HTTPProbe struct {
	Port   int               `yaml:"port" json:"port"`
	Path   string            `yaml:"path" json:"path"`
	Method string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Expect Expect            `yaml:"expect,omitempty" json:"expect,omitempty"`
}

// Check is one assertion. The flat struct carries every check type's fields;
// validation enforces that only the fields relevant to Type are set.
type Check struct {
	Type string `yaml:"type" json:"type"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// snapshot_age
	MaxAge Duration `yaml:"max_age,omitempty" json:"max_age,omitempty"`

	// files_exist / checksum_sample
	Paths      []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	MinCount   int      `yaml:"min_count,omitempty" json:"min_count,omitempty"`
	MinBytes   int64    `yaml:"min_bytes,omitempty" json:"min_bytes,omitempty"`
	SourceRoot string   `yaml:"source_root,omitempty" json:"source_root,omitempty"`
	Sample     int      `yaml:"sample,omitempty" json:"sample,omitempty"`
	Seed       int64    `yaml:"seed,omitempty" json:"seed,omitempty"`

	// size_trend
	MaxDropPercent float64 `yaml:"max_drop_percent,omitempty" json:"max_drop_percent,omitempty"`

	// sqlite_integrity / sqlite_query
	Path  string `yaml:"path,omitempty" json:"path,omitempty"`
	Query string `yaml:"query,omitempty" json:"query,omitempty"`

	// postgres_restore
	Dump    string         `yaml:"dump,omitempty" json:"dump,omitempty"`
	Format  string         `yaml:"format,omitempty" json:"format,omitempty"`
	Image   string         `yaml:"image,omitempty" json:"image,omitempty"`
	Queries []SQLQuery     `yaml:"queries,omitempty" json:"queries,omitempty"`

	// command
	Service string   `yaml:"service,omitempty" json:"service,omitempty"`
	Run     []string `yaml:"run,omitempty" json:"run,omitempty"`

	// http
	Port    int               `yaml:"port,omitempty" json:"port,omitempty"`
	Method  string            `yaml:"method,omitempty" json:"method,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`

	// service_healthy
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	Expect Expect `yaml:"expect,omitempty" json:"expect,omitempty"`

	// keys records which YAML keys were present, for strict per-type
	// validation. Not serialized.
	keys map[string]bool `yaml:"-" json:"-"`
}

// SQLQuery is one query run against a restored database.
type SQLQuery struct {
	SQL    string `yaml:"sql" json:"sql"`
	Expect Expect `yaml:"expect,omitempty" json:"expect,omitempty"`
}

// Expect is the shared expectation syntax. Only keys relevant to a check type
// are allowed; validation rejects the rest.
type Expect struct {
	Status   int     `yaml:"status,omitempty" json:"status,omitempty"`
	Min      *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max      *float64 `yaml:"max,omitempty" json:"max,omitempty"`
	Equals   string  `yaml:"equals,omitempty" json:"equals,omitempty"`
	Contains string  `yaml:"contains,omitempty" json:"contains,omitempty"`
	Regex    string  `yaml:"regex,omitempty" json:"regex,omitempty"`
	ExitCode *int    `yaml:"exit_code,omitempty" json:"exit_code,omitempty"`
	JSON     *JSONExpect `yaml:"json,omitempty" json:"json,omitempty"`

	keys map[string]bool `yaml:"-" json:"-"`
}

// JSONExpect asserts against a JSON document using a simple JSONPath subset
// ($.field.subfield[0]).
type JSONExpect struct {
	Path   string `yaml:"path" json:"path"`
	Exists *bool  `yaml:"exists,omitempty" json:"exists,omitempty"`
	Equals string `yaml:"equals,omitempty" json:"equals,omitempty"`
}

// HasKey reports whether the check's YAML explicitly set key.
func (c *Check) HasKey(key string) bool { return c.keys[key] }

// HasKey reports whether the expectation's YAML explicitly set key.
func (e *Expect) HasKey(key string) bool { return e.keys[key] }