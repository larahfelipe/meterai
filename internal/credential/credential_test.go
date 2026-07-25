package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validDocument mirrors the field set observed in a live Claude CLI
// installation on 2026-07-25. Token values are synthetic.
const validDocument = `{
  "claudeAiOauth": {
    "accessToken": "sk-ant-oat01-ACCESS",
    "refreshToken": "sk-ant-ort01-REFRESH",
    "expiresAt": 1785022579691,
    "refreshTokenExpiresAt": 1787134754691,
    "scopes": ["user:profile", "user:inference"],
    "subscriptionType": "pro",
    "rateLimitTier": "default_claude_pro"
  },
  "organizationUuid": "818f4f00-0000-4000-8000-000000000000"
}`

func TestParseAcceptsObservedSchema(t *testing.T) {
	got, err := Parse([]byte(validDocument), "test")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.AccessToken.Reveal() != "sk-ant-oat01-ACCESS" {
		t.Errorf("access token not preserved")
	}
	// expiresAt is epoch milliseconds; interpreting it as seconds would land
	// in the year 58539 and silently disable every expiry check.
	wantExpiry := time.UnixMilli(1785022579691).UTC()
	if !got.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, wantExpiry)
	}
	if got.SubscriptionType != "pro" {
		t.Errorf("plan label not preserved: %+v", got)
	}
}

func TestParseDoesNotRetainTheRefreshToken(t *testing.T) {
	// The app never refreshes, so the long-lived secret must not survive
	// parsing into a struct that lives for the whole session.
	got, err := Parse([]byte(validDocument), "test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", *got), "REFRESH") {
		t.Error("the refresh token reached the in-memory credentials")
	}
}

func TestParseIgnoresUnknownFields(t *testing.T) {
	doc := `{"claudeAiOauth":{"accessToken":"a","expiresAt":1,"futureField":{"x":1}},"tangelo":null}`
	if _, err := Parse([]byte(doc), "test"); err != nil {
		t.Fatalf("unknown fields must not break parsing: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct {
		doc  string
		kind FailureKind
	}{
		"not json":        {`not json`, Malformed},
		"empty object":    {`{}`, Incomplete},
		"missing token":   {`{"claudeAiOauth":{"expiresAt":1785022579691}}`, Incomplete},
		"zero expiry":     {`{"claudeAiOauth":{"accessToken":"a","expiresAt":0}}`, Incomplete},
		"negative expiry": {`{"claudeAiOauth":{"accessToken":"a","expiresAt":-1}}`, Incomplete},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc), "test")
			var f *Failure
			if !errors.As(err, &f) {
				t.Fatalf("want *Failure, got %v", err)
			}
			if f.Kind != tc.kind {
				t.Errorf("Kind = %v, want %v", f.Kind, tc.kind)
			}
		})
	}
}

func TestSecretNeverRenders(t *testing.T) {
	const token = "sk-ant-oat01-LIVE-TOKEN"
	c, err := Parse([]byte(`{"claudeAiOauth":{"accessToken":"`+token+`","expiresAt":1785022579691}}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{
		string(encoded),
		// fmt verbs used by logging and by error wrapping.
		strings.Join([]string{
			fmt.Sprintf("%v", c.AccessToken),
			fmt.Sprintf("%+v", *c),
			fmt.Sprintf("%#v", c.AccessToken),
		}, " "),
	} {
		if strings.Contains(rendered, token) {
			t.Fatalf("token leaked into %q", rendered)
		}
	}
}

func TestIsUsableAt(t *testing.T) {
	expiry := time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)
	c := &Credentials{ExpiresAt: expiry}
	const skew = 5 * time.Minute
	if !c.IsUsableAt(expiry.Add(-6*time.Minute), skew) {
		t.Error("token with more than the skew margin left must be usable")
	}
	if c.IsUsableAt(expiry.Add(-4*time.Minute), skew) {
		t.Error("token inside the skew margin must be treated as expired")
	}
	if c.IsUsableAt(expiry.Add(time.Second), skew) {
		t.Error("expired token must not be usable")
	}
}

func TestLoadAbsentIsSkippable(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nothing-here.json"))
	if !IsAbsent(err) {
		t.Fatalf("missing file must be skippable, got %v", err)
	}
}

func TestLoadBoundsFileSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), credentialFileName)
	oversized := make([]byte, maxCredentialBytes+1)
	for i := range oversized {
		oversized[i] = ' '
	}
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	var f *Failure
	if !errors.As(err, &f) || f.Kind != Malformed {
		t.Fatalf("oversized file must be rejected as malformed, got %v", err)
	}
}

func TestLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), credentialFileName)
	if err := os.WriteFile(path, []byte(validDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != path {
		t.Errorf("Source = %q, want %q", got.Source, path)
	}
}
