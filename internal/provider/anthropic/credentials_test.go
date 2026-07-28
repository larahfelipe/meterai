package anthropic

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
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

func TestDecodeCredentialsAcceptsObservedSchema(t *testing.T) {
	got, err := DecodeCredentials([]byte(validDocument), "test")
	if err != nil {
		t.Fatalf("DecodeCredentials: %v", err)
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

func TestDecodeCredentialsDoesNotRetainTheRefreshToken(t *testing.T) {
	// The app never refreshes, so the long-lived secret must not survive
	// parsing into a struct that lives for the whole session.
	got, err := DecodeCredentials([]byte(validDocument), "test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", *got), "REFRESH") {
		t.Error("the refresh token reached the in-memory credentials")
	}
}

func TestDecodeCredentialsIgnoresUnknownFields(t *testing.T) {
	doc := `{"claudeAiOauth":{"accessToken":"a","expiresAt":1,"futureField":{"x":1}},"tangelo":null}`
	if _, err := DecodeCredentials([]byte(doc), "test"); err != nil {
		t.Fatalf("unknown fields must not break decoding: %v", err)
	}
}

func TestDecodeCredentialsRejects(t *testing.T) {
	cases := map[string]struct {
		doc  string
		kind credential.FailureKind
	}{
		"not json":        {`not json`, credential.Malformed},
		"empty object":    {`{}`, credential.Incomplete},
		"missing token":   {`{"claudeAiOauth":{"expiresAt":1785022579691}}`, credential.Incomplete},
		"zero expiry":     {`{"claudeAiOauth":{"accessToken":"a","expiresAt":0}}`, credential.Incomplete},
		"negative expiry": {`{"claudeAiOauth":{"accessToken":"a","expiresAt":-1}}`, credential.Incomplete},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeCredentials([]byte(tc.doc), "test")
			var f *credential.Failure
			if !errors.As(err, &f) {
				t.Fatalf("want *credential.Failure, got %v", err)
			}
			if f.Kind != tc.kind {
				t.Errorf("Kind = %v, want %v", f.Kind, tc.kind)
			}
		})
	}
}
