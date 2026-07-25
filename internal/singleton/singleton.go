// Package singleton enforces one running instance per user session. Two copies
// of this app would double the polling rate against endpoints that are
// undocumented and rate-limited, and would race each other re-reading the same
// credential file, so the guard is a correctness requirement rather than a
// convenience.
package singleton

import "errors"

// ErrAlreadyRunning reports that another instance holds the guard. Callers are
// expected to exit quietly: the running instance already has a tray icon, so a
// second one has nothing useful to show.
var ErrAlreadyRunning = errors.New("another instance of meterAI is already running in this session")

// Release relinquishes the guard. It is safe to call exactly once, from the
// process that acquired it.
type Release func() error
