//go:build !windows

package singleton

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockPath is a variable so tests can isolate themselves from a real instance
// running on the development host.
var lockPath = filepath.Join(os.TempDir(), "meterAI.lock")

// Acquire takes the single-instance guard using an advisory flock. Unlike a
// PID file, the lock is released by the kernel when the process dies, so a
// crash cannot strand it.
//
// This path exists for development on Linux; the shipping target is Windows,
// where the guard is a named kernel mutex.
func Acquire() (Release, error) {
	// O_NOFOLLOW: the path is a fixed name in a world-writable directory, so
	// another account can plant a symlink there and have this process open a file
	// of its choosing. Refusing to follow one costs nothing here, since the file
	// this creates is never anything but a lock.
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock %q: %w", lockPath, err)
	}
	return func() error {
		// Closing the descriptor drops the flock; unlocking first makes the
		// intent explicit and surfaces an error if the descriptor was already
		// invalidated.
		if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
			_ = file.Close()
			return fmt.Errorf("unlock %q: %w", lockPath, err)
		}
		return file.Close()
	}, nil
}
