// Package credential locates and reads the OAuth credential file an official
// vendor CLI has already written. It is vendor-neutral: which file, and what
// its bytes mean, arrive as a RelPath and a Decoder that
// internal/provider/<vendor> supplies, while the discovery, the bounded read
// and the caching around them are one implementation shared by every vendor.
//
// It is strictly read-only, for every vendor. This process never refreshes nor
// rewrites the file: these refresh tokens are assumed to rotate on use, so a
// rotation performed here would invalidate the copy the CLI still holds and log
// the user out of their own CLI. Expiry degrades instead — the poller slows and
// the UI names that vendor's own renewal command.
package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// redactedPlaceholder replaces secret material in any rendered form of a
// credential, so that a stray %v or json.Marshal cannot leak a live token.
const redactedPlaceholder = "<redacted>"

// Secret is a token that must never reach a log, an error message, or the
// local HTTP surface. Only Reveal returns the underlying bytes.
type Secret string

func (Secret) String() string               { return redactedPlaceholder }
func (Secret) GoString() string             { return redactedPlaceholder }
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redactedPlaceholder) }

// Reveal returns the raw token. Every call site is an outbound HTTPS header.
func (s Secret) Reveal() string { return string(s) }

// FailureKind classifies a credential failure. Absent and Unreadable mean
// "try the next candidate path"; the rest mean "stop and tell the user".
type FailureKind uint8

const (
	// Absent: no file at this path. Expected during candidate enumeration.
	Absent FailureKind = iota + 1
	// Unreadable: the path exists but could not be read (permissions, WSL
	// distro unavailable, UNC transport failure).
	Unreadable
	// Malformed: the bytes are not the JSON document this parser accepts.
	Malformed
	// Incomplete: valid JSON, but a field required to authenticate is missing
	// or empty. Indicates a CLI schema change.
	Incomplete
	// Expired: the file parsed cleanly but its access token is past its usable
	// window. Only the vendor's own CLI can renew it; this process never does.
	Expired
)

func (k FailureKind) String() string {
	switch k {
	case Absent:
		return "absent"
	case Unreadable:
		return "unreadable"
	case Malformed:
		return "malformed"
	case Incomplete:
		return "incomplete"
	case Expired:
		return "expired"
	default:
		return "unknown"
	}
}

// Failure is the error type of this package.
type Failure struct {
	Kind FailureKind
	Path string
	Err  error
}

func (f *Failure) Error() string {
	return fmt.Sprintf("credential %s at %q: %v", f.Kind, f.Path, f.Err)
}

func (f *Failure) Unwrap() error { return f.Err }

// IsAbsent reports whether err means "nothing here, keep looking".
func IsAbsent(err error) bool {
	var f *Failure
	return errors.As(err, &f) && (f.Kind == Absent || f.Kind == Unreadable)
}

// Credentials carries only what this app actually uses. The refresh token is
// deliberately not among them: since the app never refreshes, holding a
// long-lived secret in process memory for the whole session would be exposure
// bought for nothing.
type Credentials struct {
	AccessToken Secret
	ExpiresAt   time.Time
	// SubscriptionType is the vendor's plan label, shown for context only.
	SubscriptionType string
	// AccountID is optional, vendor-specific auth metadata beyond the bearer
	// token itself — some APIs require it as a sibling header (OpenAI's
	// ChatGPT-Account-Id). Empty when the vendor's API needs none, as with
	// Anthropic today.
	AccountID string
	// Source is the path these bytes came from, both for diagnostics and for
	// re-reading directly when the access token nears expiry.
	Source string
}

// IsUsableAt reports whether the access token can still authenticate a request
// issued at t, leaving skewMargin of headroom for clock skew between this host
// and the vendor's edge.
func (c *Credentials) IsUsableAt(t time.Time, skewMargin time.Duration) bool {
	return t.Add(skewMargin).Before(c.ExpiresAt)
}

// RelPath identifies a vendor CLI's credential file by the directory and file
// name appended to a home directory, native or reached over WSL. Discovery
// stays vendor-neutral by taking this as a parameter rather than naming a
// vendor's own layout: internal/provider/<vendor> owns the literal values.
type RelPath struct {
	Dir  string
	File string
}

// Decoder turns raw credential bytes into Credentials. Each vendor supplies
// its own: the on-disk schema differs (JSON key names, epoch-millis vs.
// RFC3339 expiry), while the bounded read, discovery and caching around it do
// not.
type Decoder func(raw []byte, path string) (*Credentials, error)

// maxCredentialBytes bounds the read so a corrupted or hostile file cannot
// exhaust memory. The real file is well under a kilobyte.
const maxCredentialBytes = 1 << 20

// Load reads and decodes one candidate path. A missing file yields Absent, so
// the caller can advance to the next candidate.
func Load(path string, decode Decoder) (*Credentials, error) {
	f, err := os.Open(path)
	if err != nil {
		kind := Unreadable
		if errors.Is(err, os.ErrNotExist) {
			kind = Absent
		}
		return nil, &Failure{Kind: kind, Path: path, Err: err}
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil {
		return nil, &Failure{Kind: Unreadable, Path: path, Err: err}
	}
	if len(raw) > maxCredentialBytes {
		return nil, &Failure{Kind: Malformed, Path: path,
			Err: fmt.Errorf("file exceeds %d byte limit", maxCredentialBytes)}
	}
	return decode(raw, path)
}
