package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/bcrypt"
)

// Validate checks schema and semantics. It returns all problems found, each
// phrased as cause + next step. Warnings (secret file permissions, unpinned
// images) are appended to c.Warnings instead.
func Validate(c *Config) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.Version != SchemaVersion {
		add("version must be %d — set \"version: 1\"", SchemaVersion)
	}
	if c.Retention.RunsPerDrill < 1 {
		add("retention.runs_per_drill must be >= 1")
	}
	if c.Defaults.Timeout.D() <= 0 {
		add("defaults.timeout must be positive")
	}
	if c.Defaults.KeepTTL.D() < 0 {
		add("defaults.keep_ttl must not be negative")
	}
	if c.Defaults.Resources != nil {
		validateResources(c.Defaults.Resources, "defaults.resources", add)
	}

	for name, repo := range c.Repositories {
		validateRepository(name, repo, c.AllowInlineSecrets, add)
	}
	for name, n := range c.Notifiers {
		validateNotifier(name, n, c.AllowInlineSecrets, add)
	}
	validateServer(c, add)

	for _, name := range c.DrillNames() {
		d := c.Drills[name]
		validateDrill(c, name, d, add)
	}

	checkSecretFilePerms(c)
	return errs
}

func validateRepository(name string, r Repository, allowInline bool, add func(string, ...any)) {
	where := "repositories." + name
	switch r.Type {
	case "restic", "borg":
	case "":
		add("%s: type is required — set type: restic or type: borg", where)
	default:
		add("%s: unknown type %q — supported: restic, borg", where, r.Type)
	}
	if r.Repository == "" {
		add("%s: repository is required — set the repository URL or path", where)
	}
	if r.NoLock && r.Type != "restic" {
		add("%s: no_lock is only supported for restic repositories", where)
	}
	inline := 0
	if r.Password != "" {
		inline++
	}
	if r.Passphrase != "" {
		inline++
	}
	if inline > 0 && !allowInline {
		add("%s: inline secrets are not allowed — use password_file/password_command (restic) or passcommand/env (borg), or set allow_inline_secrets: true", where)
	}
	if r.Type == "restic" {
		set := 0
		for _, v := range []string{r.PasswordFile, r.PasswordCommand, r.Password} {
			if v != "" {
				set++
			}
		}
		if set > 1 {
			add("%s: set at most one of password_file, password_command, password", where)
		}
		if r.Passcommand != "" || r.Passphrase != "" {
			add("%s: passcommand/passphrase are borg options — use password_file or password_command for restic", where)
		}
	}
	if r.Type == "borg" {
		if r.PasswordFile != "" || r.PasswordCommand != "" || r.Password != "" {
			add("%s: password_file/password_command/password are restic options — use passcommand or BORG_PASSPHRASE in env for borg", where)
		}
		if r.Passcommand != "" && r.Passphrase != "" {
			add("%s: set at most one of passcommand, passphrase", where)
		}
	}
}

func validateNotifier(name string, n Notifier, allowInline bool, add func(string, ...any)) {
	where := "notifiers." + name
	switch n.Type {
	case "ntfy":
		if n.URL == "" {
			add("%s: url is required for ntfy", where)
		}
	case "matrix":
		if n.Homeserver == "" || n.RoomID == "" {
			add("%s: homeserver and room_id are required for matrix", where)
		}
		if n.Token == "" && n.TokenFile == "" {
			add("%s: matrix requires token_file (or token)", where)
		}
	case "webhook":
		if n.URL == "" {
			add("%s: url is required for webhook", where)
		}
	case "":
		add("%s: type is required — supported: ntfy, matrix, webhook", where)
	default:
		add("%s: unknown type %q — supported: ntfy, matrix, webhook", where, n.Type)
	}
	if n.URL != "" {
		if u, err := url.Parse(n.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			add("%s: url must be an http(s) URL", where)
		}
	}
	if n.Homeserver != "" {
		if u, err := url.Parse(n.Homeserver); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			add("%s: homeserver must be an http(s) URL", where)
		}
	}
	if (n.Token != "" || n.Secret != "") && !allowInline {
		add("%s: inline secrets are not allowed — use token_file/secret_file, or set allow_inline_secrets: true", where)
	}
	valid := map[string]bool{"pass": true, "fail": true, "error": true, "recovered": true}
	for _, ev := range n.On {
		if !valid[ev] {
			add("%s: unknown event %q in on: — supported: pass, fail, error, recovered", where, ev)
		}
	}
	if n.RenotifyAfter.D() < 0 {
		add("%s: renotify_after must not be negative", where)
	}
}

func validateServer(c *Config, add func(string, ...any)) {
	s := c.Server
	if _, _, err := net.SplitHostPort(s.Listen); err != nil {
		add("server.listen %q is not host:port — e.g. 127.0.0.1:8484", s.Listen)
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		add("server.timezone %q is unknown — use an IANA name like UTC", s.Timezone)
	}
	switch s.Auth.Mode {
	case "none":
		if host, _, err := net.SplitHostPort(s.Listen); err == nil && !isLoopbackHost(host) {
			add("server.listen binds to non-loopback %q with auth.mode none — set auth.mode basic or trusted_header, or bind to 127.0.0.1", host)
		}
	case "basic":
		if len(s.Auth.Users) == 0 {
			add("server.auth.mode basic requires at least one user under server.auth.users")
		}
		for user, u := range s.Auth.Users {
			if u.PasswordHash == "" {
				add("server.auth.users.%s: password_hash is required (bcrypt)", user)
				continue
			}
			if _, err := bcrypt.Cost([]byte(u.PasswordHash)); err != nil {
				add("server.auth.users.%s: password_hash is not a valid bcrypt hash — generate one with: htpasswd -nB <user>", user)
			}
		}
	case "trusted_header":
		if len(s.Auth.TrustedProxies) == 0 {
			add("server.auth.mode trusted_header requires trusted_proxies CIDRs — e.g. [127.0.0.1/32]")
		}
		for _, cidr := range s.Auth.TrustedProxies {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				add("server.auth.trusted_proxies: %q is not a CIDR — e.g. 10.0.0.0/8", cidr)
			}
		}
	default:
		add("server.auth.mode %q is unknown — supported: none, basic, trusted_header", s.Auth.Mode)
	}
	if s.PublicURL != "" {
		if u, err := url.Parse(s.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			add("server.public_url must be an http(s) URL")
		}
	}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateResources(r *Resources, where string, add func(string, ...any)) {
	if r.CPUs < 0 {
		add("%s: cpus must not be negative", where)
	}
	if r.Memory != "" {
		if _, err := ParseBytes(r.Memory); err != nil {
			add("%s: memory: %v", where, err)
		}
	}
}

var drillNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func validateDrill(c *Config, name string, d Drill, add func(string, ...any)) {
	where := "drills." + name
	if !drillNameRE.MatchString(name) {
		add("%s: drill names must match [a-zA-Z0-9][a-zA-Z0-9_-]*", where)
	}
	if d.Uses != "" {
		add("%s: uses: was not resolved — this is a bug, please report it", where)
	}
	repo, ok := c.Repositories[d.Repository]
	if d.Repository == "" {
		add("%s: repository is required — reference one of the configured repositories", where)
	} else if !ok {
		add("%s: repository %q is not defined under repositories:", where, d.Repository)
	}
	if d.Schedule != "" {
		if _, err := cron.ParseStandard(d.Schedule); err != nil {
			add("%s: schedule %q is not a valid 5-field cron expression: %v", where, d.Schedule, err)
		}
	}
	if d.Jitter.D() < 0 {
		add("%s: jitter must not be negative", where)
	}
	if d.Timeout.D() <= 0 {
		add("%s: timeout must be positive", where)
	}
	for _, n := range d.Notify {
		if _, ok := c.Notifiers[n]; !ok {
			add("%s: notify references unknown notifier %q — define it under notifiers:", where, n)
		}
	}
	if d.Resources != nil {
		validateResources(d.Resources, where+".resources", add)
	}

	// Snapshot selector.
	switch d.Snapshot.Select {
	case "latest", "random", "oldest":
	default:
		add("%s: snapshot.select %q is unknown — supported: latest, random, oldest", where, d.Snapshot.Select)
	}
	if d.Snapshot.Within.D() != 0 && d.Snapshot.Select == "latest" {
		add("%s: snapshot.within only applies to select: random or oldest", where)
	}
	if d.Snapshot.Within.D() < 0 {
		add("%s: snapshot.within must not be negative", where)
	}
	if d.Snapshot.MaxAge.D() < 0 {
		add("%s: snapshot.max_age must not be negative", where)
	}
	if d.Snapshot.ArchiveGlob != "" && ok && repo.Type != "borg" {
		add("%s: snapshot.archive_glob only applies to borg repositories", where)
	}
	if len(d.Snapshot.Tags) > 0 && ok && repo.Type != "restic" {
		add("%s: snapshot.tags only applies to restic repositories (borg has no tags)", where)
	}

	// Restore paths.
	if len(d.Restore.Paths) == 0 {
		add("%s: restore.paths must list at least one absolute path", where)
	}
	for _, p := range d.Restore.Paths {
		if !filepath.IsAbs(p) {
			add("%s: restore.paths entry %q must be absolute — restored files keep their original absolute path below the restore root", where, p)
		}
	}

	// Sandbox.
	if d.Sandbox != nil {
		validateSandbox(c, where, d, add)
	}

	// Checks.
	for i, ch := range d.Checks {
		validateCheck(c, where, d, i, ch, add)
	}
}

func validateSandbox(c *Config, where string, d Drill, add func(string, ...any)) {
	sb := d.Sandbox
	if sb.Network != "isolated" {
		add("%s: sandbox.network %q is not supported — only \"isolated\" (no egress) is allowed", where, sb.Network)
	}
	if len(sb.Services) == 0 {
		add("%s: sandbox.services must define at least one service", where)
	}
	for name, svc := range sb.Services {
		swhere := where + ".sandbox.services." + name
		if svc.Image == "" {
			add("%s: image is required", swhere)
		} else {
			warnUnpinnedImage(c, swhere, svc.Image)
		}
		for _, m := range svc.Mounts {
			if m.Source == "" || m.Target == "" {
				add("%s: mounts need source and target", swhere)
			}
			if m.Target != "" && !filepath.IsAbs(m.Target) {
				add("%s: mount target %q must be absolute", swhere, m.Target)
			}
		}
		if svc.Resources != nil {
			validateResources(svc.Resources, swhere+".resources", add)
		}
		if svc.Healthcheck != nil {
			hc := svc.Healthcheck
			if hc.HTTP == nil {
				add("%s: healthcheck needs an http probe (only http healthchecks are supported in v1)", swhere)
			} else {
				if hc.HTTP.Port < 1 || hc.HTTP.Port > 65535 {
					add("%s: healthcheck.http.port must be 1-65535", swhere)
				}
				validateExpectKeys(hc.HTTP.Expect, "healthcheck", swhere+".healthcheck.http.expect", add)
			}
			if hc.Timeout.D() < 0 {
				add("%s: healthcheck.timeout must not be negative", swhere)
			}
		}
	}
}

func warnUnpinnedImage(c *Config, where, image string) {
	// Digest-pinned images contain @sha256:...
	if strings.Contains(image, "@") {
		return
	}
	tag := ""
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		tag = image[i+1:]
	}
	if tag == "" || tag == "latest" {
		c.Warnings = append(c.Warnings, fmt.Sprintf("%s: image %q is not pinned — pin a tag or digest for reproducible drills", where, image))
	}
}

// checkSpec describes the allowed and required keys of one check type.
type checkSpec struct {
	allowed  []string
	required []string
	expect   string // expectation profile: "", "scalar", "command", "http"
	phase    string
}

var checkSpecs = map[string]checkSpec{
	"snapshot_age":     {allowed: []string{"max_age"}, required: []string{"max_age"}, phase: "data"},
	"files_exist":      {allowed: []string{"paths", "min_count", "min_bytes"}, required: []string{"paths"}, phase: "data"},
	"size_trend":       {allowed: []string{"max_drop_percent"}, required: []string{"max_drop_percent"}, phase: "data"},
	"checksum_sample":  {allowed: []string{"source_root", "paths", "sample", "seed"}, required: []string{"source_root", "paths"}, phase: "data"},
	"sqlite_integrity": {allowed: []string{"path"}, required: []string{"path"}, phase: "data"},
	"sqlite_query":     {allowed: []string{"path", "query", "expect"}, required: []string{"path", "query", "expect"}, expect: "scalar", phase: "data"},
	"postgres_restore": {allowed: []string{"dump", "format", "image", "queries"}, required: []string{"dump"}, phase: "data"},
	"command":          {allowed: []string{"image", "service", "run", "expect"}, required: []string{"run"}, expect: "command", phase: "data"},
	"service_healthy":  {allowed: []string{"service", "timeout"}, required: []string{"service"}, phase: "service"},
	"http":             {allowed: []string{"service", "port", "path", "method", "headers", "expect"}, required: []string{"service", "port"}, expect: "http", phase: "service"},
}

// CheckPhase returns the phase ("data" or "service") of a check type.
func CheckPhase(typ string) string {
	if s, ok := checkSpecs[typ]; ok {
		return s.phase
	}
	return "data"
}

// CheckTypes lists all registered check type names.
func CheckTypes() []string {
	names := make([]string, 0, len(checkSpecs))
	for k := range checkSpecs {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func validateCheck(c *Config, where string, d Drill, idx int, ch Check, add func(string, ...any)) {
	cwhere := fmt.Sprintf("%s.checks[%d]", where, idx)
	spec, ok := checkSpecs[ch.Type]
	if !ok {
		add("%s: unknown check type %q — supported: %s", cwhere, ch.Type, strings.Join(CheckTypes(), ", "))
		return
	}
	allowed := map[string]bool{"type": true, "name": true}
	for _, k := range spec.allowed {
		allowed[k] = true
	}
	keys := make([]string, 0, len(ch.keys))
	for k := range ch.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			add("%s: field %q is not valid for check type %q — allowed: %s", cwhere, k, ch.Type, strings.Join(spec.allowed, ", "))
		}
	}
	for _, k := range spec.required {
		if !ch.keys[k] {
			add("%s: check type %q requires field %q", cwhere, ch.Type, k)
		}
	}

	switch ch.Type {
	case "files_exist":
		if ch.MinCount < 0 || ch.MinBytes < 0 {
			add("%s: min_count/min_bytes must not be negative", cwhere)
		}
		for _, p := range ch.Paths {
			if filepath.IsAbs(p) {
				add("%s: paths must be relative to the restore root, got %q", cwhere, p)
			}
		}
	case "checksum_sample":
		if !filepath.IsAbs(ch.SourceRoot) {
			add("%s: source_root must be absolute (the live filesystem root to compare against)", cwhere)
		}
		if ch.Sample < 0 {
			add("%s: sample must not be negative", cwhere)
		}
		for _, p := range ch.Paths {
			if filepath.IsAbs(p) {
				add("%s: paths must be relative to the restore root, got %q", cwhere, p)
			}
		}
	case "size_trend":
		if ch.MaxDropPercent < 0 || ch.MaxDropPercent > 100 {
			add("%s: max_drop_percent must be between 0 and 100", cwhere)
		}
	case "sqlite_integrity", "sqlite_query":
		if filepath.IsAbs(ch.Path) {
			add("%s: path must be relative to the restore root, got %q", cwhere, ch.Path)
		}
	case "postgres_restore":
		if filepath.IsAbs(ch.Dump) {
			add("%s: dump must be relative to the restore root, got %q", cwhere, ch.Dump)
		}
		switch ch.Format {
		case "", "custom", "plain", "directory":
		default:
			add("%s: format %q is unknown — supported: custom, plain, directory", cwhere, ch.Format)
		}
		if ch.Image != "" {
			warnUnpinnedImage(c, cwhere, ch.Image)
		}
		for qi, q := range ch.Queries {
			if q.SQL == "" {
				add("%s.queries[%d]: sql is required", cwhere, qi)
			}
			validateExpectKeys(q.Expect, "scalar", fmt.Sprintf("%s.queries[%d].expect", cwhere, qi), add)
		}
	case "command":
		hasImage, hasService := ch.keys["image"], ch.keys["service"]
		if hasImage == hasService {
			add("%s: set exactly one of image or service", cwhere)
		}
		if hasService && !serviceDefined(d, ch.Service) {
			add("%s: service %q is not defined in sandbox.services", cwhere, ch.Service)
		}
		if hasImage {
			warnUnpinnedImage(c, cwhere, ch.Image)
		}
		if len(ch.Run) == 0 {
			add("%s: run must not be empty", cwhere)
		}
	case "service_healthy":
		if !serviceDefined(d, ch.Service) {
			add("%s: service %q is not defined in sandbox.services", cwhere, ch.Service)
		}
	case "http":
		if !serviceDefined(d, ch.Service) {
			add("%s: service %q is not defined in sandbox.services", cwhere, ch.Service)
		}
		if ch.Port < 1 || ch.Port > 65535 {
			add("%s: port must be 1-65535", cwhere)
		}
		if ch.Method != "" {
			switch strings.ToUpper(ch.Method) {
			case "GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS", "PATCH":
			default:
				add("%s: method %q is not supported", cwhere, ch.Method)
			}
		}
	}
	if spec.expect != "" {
		validateExpectKeys(ch.Expect, spec.expect, cwhere+".expect", add)
	} else if len(ch.Expect.keys) > 0 {
		add("%s: check type %q does not accept expect:", cwhere, ch.Type)
	}
}

func serviceDefined(d Drill, name string) bool {
	if d.Sandbox == nil {
		return false
	}
	_, ok := d.Sandbox.Services[name]
	return ok
}

var expectProfiles = map[string][]string{
	"scalar":      {"min", "max", "equals"},
	"command":     {"exit_code", "equals", "contains", "regex"},
	"http":        {"status", "equals", "contains", "regex", "json"},
	"healthcheck": {"status", "equals", "contains", "regex", "json"},
}

func validateExpectKeys(e Expect, profile, where string, add func(string, ...any)) {
	allowed := map[string]bool{}
	for _, k := range expectProfiles[profile] {
		allowed[k] = true
	}
	keys := make([]string, 0, len(e.keys))
	for k := range e.keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			add("%s: expectation key %q is not valid here — allowed: %s", where, k, strings.Join(expectProfiles[profile], ", "))
		}
	}
	if e.Regex != "" {
		if _, err := regexp.Compile(e.Regex); err != nil {
			add("%s: regex is invalid: %v", where, err)
		}
	}
	if e.JSON != nil && e.JSON.Path == "" {
		add("%s: json.path is required", where)
	}
	if e.Min != nil && e.Max != nil && *e.Min > *e.Max {
		add("%s: min must not be greater than max", where)
	}
}