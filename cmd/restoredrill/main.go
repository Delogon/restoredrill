// Command restoredrill proves backups can actually be restored.
package main

import (
	"errors"
	"fmt"
	"os"
)

// Build information, overridden at link time.
var (
	version = "0.1.0-dev"
	commit  = "none"
)

// Exit codes (for cron, systemd and CI).
const (
	exitOK       = 0 // all runs passed
	exitFail     = 1 // at least one run failed
	exitUsage    = 2 // config or usage error
	exitError    = 3 // at least one run errored, none failed
	exitInternal = 4 // internal error
)

// exitCodeError carries a specific process exit code.
type exitCodeError struct {
	code int
	msg  string
}

func (e exitCodeError) Error() string { return e.msg }

func main() {
	err := execute()
	if err == nil {
		os.Exit(exitOK)
	}
	var ec exitCodeError
	if errors.As(err, &ec) {
		if ec.msg != "" {
			fmt.Fprintln(os.Stderr, "restoredrill: "+ec.msg)
		}
		os.Exit(ec.code)
	}
	fmt.Fprintln(os.Stderr, "restoredrill: "+err.Error())
	os.Exit(exitInternal)
}