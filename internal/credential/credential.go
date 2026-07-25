// Package credential locates and parses the OAuth credential file written by
// the official Claude CLI. It is strictly read-only: this process never
// refreshes nor rewrites the file, because the Anthropic refresh_token is
// assumed to rotate on use and a rotation performed here would invalidate the
// copy the CLI still holds, logging the user out of their CLI.
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
	// Source is the path these bytes came from, both for diagnostics and for
	// re-reading directly when the access token nears expiry.
	Source string
}

// IsUsableAt reports whether the access token can still authenticate a request
// issued at t, leaving skewMargin of headroom for clock skew between this host
// and Anthropic's edge.
func (c *Credentials) IsUsableAt(t time.Time, skewMargin time.Duration) bool {
	return t.Add(skewMargin).Before(c.ExpiresAt)
}

// document records the on-disk schema, which the CLI publishes nowhere and
// which was established by inspection. Fields the app does not use are listed
// so the format stays documented, but only the used ones reach Credentials.
// The expiry values are epoch MILLISECONDS, not seconds.
type document struct {
	ClaudeAiOauth struct {
		AccessToken           string   `json:"accessToken"`
		RefreshToken          string   `json:"refreshToken"`
		ExpiresAtMillis       int64    `json:"expiresAt"`
		RefreshExpiresAtMilli int64    `json:"refreshTokenExpiresAt"`
		Scopes                []string `json:"scopes"`
		SubscriptionType      string   `json:"subscriptionType"`
		RateLimitTier         string   `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
	OrganizationUUID string `json:"organizationUuid"`
}

// maxCredentialBytes bounds the read so a corrupted or hostile file cannot
// exhaust memory. The real file is well under a kilobyte.
const maxCredentialBytes = 1 << 20

// Parse validates raw credential bytes. path is used only for error context.
func Parse(raw []byte, path string) (*Credentials, error) {
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &Failure{Kind: Malformed, Path: path, Err: err}
	}
	o := doc.ClaudeAiOauth
	switch {
	case o.AccessToken == "":
		return nil, &Failure{Kind: Incomplete, Path: path, Err: errors.New("claudeAiOauth.accessToken is empty")}
	case o.ExpiresAtMillis <= 0:
		return nil, &Failure{Kind: Incomplete, Path: path, Err: errors.New("claudeAiOauth.expiresAt is not a positive epoch-millisecond value")}
	}
	return &Credentials{
		AccessToken:      Secret(o.AccessToken),
		ExpiresAt:        time.UnixMilli(o.ExpiresAtMillis).UTC(),
		SubscriptionType: o.SubscriptionType,
		Source:           path,
	}, nil
}

// Load reads and parses one candidate path. A missing file yields Absent, so
// the caller can advance to the next candidate.
func Load(path string) (*Credentials, error) {
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
	return Parse(raw, path)
}
