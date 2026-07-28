package openai

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
)

// validAuthFile mirrors the field set observed in a Codex CLI auth.json,
// confirmed against github.com/akitaonrails/ai-usagebar's openai/creds.rs.
// Token values are synthetic.
const validAuthFile = `{
  "auth_mode": "chatgpt",
  "tokens": {
    "id_token": "eyJ.SHOULD-NOT-APPEAR.eyJ",
    "access_token": "codex-access-TEST",
    "refresh_token": "codex-refresh-SHOULD-NOT-APPEAR",
    "account_id": "acct-123",
    "expires_at": "2026-07-25T23:36:19Z"
  },
  "last_refresh": "2026-07-25T18:00:00Z"
}`

func TestDecodeCredentialsAcceptsObservedSchema(t *testing.T) {
	got, err := DecodeCredentials([]byte(validAuthFile), "test")
	if err != nil {
		t.Fatalf("DecodeCredentials: %v", err)
	}
	if got.AccessToken.Reveal() != "codex-access-TEST" {
		t.Errorf("access token not preserved")
	}
	if got.AccountID != "acct-123" {
		t.Errorf("AccountID = %q, want %q", got.AccountID, "acct-123")
	}
	want := time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)
	if !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want)
	}
}

func TestDecodeCredentialsDoesNotRetainTheRefreshTokenOrIDToken(t *testing.T) {
	got, err := DecodeCredentials([]byte(validAuthFile), "test")
	if err != nil {
		t.Fatal(err)
	}
	rendered := fmt.Sprintf("%#v", *got)
	for _, secret := range []string{"SHOULD-NOT-APPEAR"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("a secret the app never uses reached the in-memory credentials: %q", rendered)
		}
	}
}

func TestDecodeCredentialsIgnoresUnknownFields(t *testing.T) {
	doc := `{"tokens":{"access_token":"a","expires_at":"2026-07-25T23:36:19Z","future_field":1},"auth_mode":"apiKey"}`
	if _, err := DecodeCredentials([]byte(doc), "test"); err != nil {
		t.Fatalf("unknown fields must not break decoding: %v", err)
	}
}

func TestDecodeCredentialsRejects(t *testing.T) {
	cases := map[string]struct {
		doc  string
		kind credential.FailureKind
	}{
		"not json":               {`not json`, credential.Malformed},
		"empty object":           {`{}`, credential.Incomplete},
		"missing access token":   {`{"tokens":{"expires_at":"2026-07-25T23:36:19Z"}}`, credential.Incomplete},
		"empty access token":     {`{"tokens":{"access_token":"","expires_at":"2026-07-25T23:36:19Z"}}`, credential.Incomplete},
		"missing expires_at":     {`{"tokens":{"access_token":"a"}}`, credential.Incomplete},
		"empty expires_at":       {`{"tokens":{"access_token":"a","expires_at":""}}`, credential.Incomplete},
		"expires_at not RFC3339": {`{"tokens":{"access_token":"a","expires_at":"1785022579691"}}`, credential.Incomplete},
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

func TestDecodeCredentialsAcceptsAnEmptyAccountID(t *testing.T) {
	// account_id absent should not fail decoding: Fetch simply omits the
	// ChatGPT-Account-Id header when it is empty.
	doc := `{"tokens":{"access_token":"a","expires_at":"2026-07-25T23:36:19Z"}}`
	got, err := DecodeCredentials([]byte(doc), "test")
	if err != nil {
		t.Fatalf("DecodeCredentials: %v", err)
	}
	if got.AccountID != "" {
		t.Errorf("AccountID = %q, want empty", got.AccountID)
	}
}
