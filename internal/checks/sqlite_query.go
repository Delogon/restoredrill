package checks

import (
	"context"
	"strconv"

	"github.com/restoredrill/restoredrill/internal/config"
)

// sqliteQuery runs a scalar query against a restored SQLite database and
// checks the result against an expectation (min / max / equals).
type sqliteQuery struct{}

func init() { Register(sqliteQuery{}) }

func (sqliteQuery) Type() string  { return "sqlite_query" }
func (sqliteQuery) Phase() string { return "data" }

func (sqliteQuery) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	path, err := resolveDBPath(c, cfg.Path)
	if err != nil {
		return failf("sqlite_query: %v", err)
	}
	db, err := openSQLiteReadOnly(path)
	if err != nil {
		return failf("sqlite_query: cannot open %s (file may be truncated or corrupt): %v", cfg.Path, err)
	}
	defer db.Close()

	var raw string
	row := db.QueryRowContext(ctx, cfg.Query)
	if err := row.Scan(&raw); err != nil {
		return failf("sqlite_query: query %q on %s failed: %v", cfg.Query, cfg.Path, err)
	}
	value, parseErr := strconv.ParseFloat(raw, 64)
	details := map[string]any{"query": cfg.Query, "result": raw}
	if parseErr != nil {
		// Non-numeric result: only equals can apply.
		if cfg.Expect.HasKey("equals") {
			if raw != cfg.Expect.Equals {
				return Result{Status: Fail, Message: sprintf("sqlite_query: result %q does not equal %q", raw, cfg.Expect.Equals), Details: details}
			}
			return Result{Status: Pass, Message: sprintf("sqlite_query: result %q matches", raw), Details: details}
		}
		if cfg.Expect.Min != nil || cfg.Expect.Max != nil {
			return errorf("sqlite_query: result %q is not numeric, cannot apply min/max", raw)
		}
		return Result{Status: Pass, Message: sprintf("sqlite_query: result %q", raw), Details: details}
	}
	if ok, msg := matchScalar(cfg.Expect, value, raw); !ok {
		return Result{Status: Fail, Message: "sqlite_query: " + msg, Details: details}
	}
	return Result{Status: Pass, Message: sprintf("sqlite_query: result %s satisfies expectation", raw), Details: details}
}