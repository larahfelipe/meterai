package credential

import (
	"bytes"
	"slices"
	"strings"
)

// This file is untagged so the WSL path derivation can be asserted on any host.
// Only the enumeration that reaches wsl.exe and the filesystem lives in
// locate_windows.go, which no automated check on a Linux host can exercise.

const (
	// wslHomeParent and wslRootHome are the two places a Linux login home can be.
	// Everything else — a home relocated to /opt, /srv or a mount — is out of
	// reach here by design and is what the credentialPath setting exists for.
	wslHomeParent = `\home`
	wslRootHome   = `\root`

	// maxWSLHomeDirs bounds how many home directories are probed in one
	// distribution. Each probe is a file open over the 9P transport, which is
	// slow enough that an unusual machine with hundreds of accounts would stall
	// discovery; the cap keeps a pathological listing from becoming a hang.
	maxWSLHomeDirs = 32

	// maxWSLOutputBytes bounds what is kept from wsl.exe. The distribution list
	// is a handful of names, but a child process is an unbounded producer: a
	// wedged or replaced one must not be able to grow this process's heap, and
	// decodeWSLOutput allocates in proportion to what it is handed.
	maxWSLOutputBytes = 64 << 10
)

// boundedBuffer keeps the first limit bytes written to it and discards the rest.
//
// It reports every write as fully consumed. Returning a short count would make
// os/exec tear the pipe down and fail the command for a reason unrelated to the
// enumeration, whereas truncating output that is already past a limit no real
// distribution list approaches is exactly the intended outcome.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	accepted := len(p)
	if room := b.limit - b.buf.Len(); room > 0 {
		if accepted > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return accepted, nil
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }

// wslCredentialPaths lists the credential files worth probing inside one
// distribution, given its UNC root.
//
// It resolves the home directories by listing them over the same file transport
// the credential itself is read through, rather than by running `sh -c 'echo
// $HOME'` inside the distribution: that spent a full VM boot to learn one string,
// and starting a shell interpreter inside another operating system is a large
// amount of machinery for a directory name. Nothing is executed inside the
// distribution any more, which keeps this app to a single child process.
//
// listDir reports the entry names of a directory; a failure means "nothing to
// probe here" and is not distinguished from an empty directory, because a
// stopped distribution and an account-less one are handled identically.
func wslCredentialPaths(distroRoot string, listDir func(string) ([]string, error)) []string {
	paths := make([]string, 0, maxWSLHomeDirs+1)

	// The login account first: the CLI is normally run as an ordinary user, and
	// the order decides which account the menu ends up describing.
	if homes, err := listDir(distroRoot + wslHomeParent); err == nil {
		// Sorted so the same machine always yields the same order; a directory
		// listing carries none of its own.
		slices.Sort(homes)
		if len(homes) > maxWSLHomeDirs {
			homes = homes[:maxWSLHomeDirs]
		}
		for _, home := range homes {
			if !isPlainName(home) {
				continue
			}
			paths = append(paths, credentialPathIn(distroRoot+wslHomeParent+`\`+home))
		}
	}
	return append(paths, credentialPathIn(distroRoot+wslRootHome))
}

func credentialPathIn(home string) string {
	return home + `\` + claudeConfigDirName + `\` + credentialFileName
}

// isPlainName rejects anything that is not a single path component. Directory
// names and distribution names both reach a path this way, and neither is
// produced by this app: one comes from a filesystem another operating system
// owns, the other from wsl.exe's output. A name carrying a separator or a parent
// reference would build a path outside the tree being searched.
func isPlainName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `\/:`)
}
