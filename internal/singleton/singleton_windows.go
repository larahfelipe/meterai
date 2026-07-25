//go:build windows

package singleton

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// guardName lives in the Local\ namespace, which is per logon session rather
// than machine-wide. That is the correct scope: each Windows user has their own
// credential file and their own subscription, so two users on one machine must
// each be able to run their own instance. A Global\ name would let one user
// silently block another.
//
// The version suffix allows a future release with incompatible IPC to coexist
// with an old one during an upgrade instead of deadlocking against it.
const guardName = `Local\meterAI.singleton.v1`

// Acquire takes the single-instance guard. The Win32 mutex is released by the
// kernel if the process crashes, so a hard kill cannot leave a stale guard
// behind the way a lock file can.
func Acquire() (Release, error) {
	name, err := windows.UTF16PtrFromString(guardName)
	if err != nil {
		return nil, fmt.Errorf("encode mutex name: %w", err)
	}

	// CreateMutexW returns a valid handle whether or not the mutex already
	// existed; only the last-error value distinguishes the two, so the error
	// must be inspected before the handle.
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return nil, ErrAlreadyRunning
	}
	if handle == 0 {
		return nil, fmt.Errorf("create single-instance mutex: %w", err)
	}
	return func() error { return windows.CloseHandle(handle) }, nil
}
