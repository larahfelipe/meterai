package openai

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
)

// makeJWT builds a syntactically valid JWT string carrying the given claims
// body verbatim, so tests can control exactly what jwtExpiry has to parse
// without depending on a real signing key. The header and signature segments
// are fixed and never inspected by this app.
func makeJWT(claimsJSON string) string {
	seg := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return seg(`{"alg":"RS256","typ":"JWT"}`) + "." + seg(claimsJSON) + "." + seg("signature")
}

func accessTokenExpiringAt(t time.Time) string {
	return makeJWT(`{"exp":` + strconv.FormatInt(t.Unix(), 10) + `}`)
}

// validAuthFile mirrors the field set of a live Codex CLI auth.json,
// confirmed directly against a real file: notably, there is no
// tokens.expires_at at all, only what the access_token JWT's own exp claim
// carries. Token values are synthetic.
var validAuthFile = fmt.Sprintf(`{
  "auth_mode": "chatgpt",
  "OPENAI_API_KEY": null,
  "tokens": {
    "id_token": "eyJ.SHOULD-NOT-APPEAR.eyJ",
    "access_token": %q,
    "refresh_token": "rt.SHOULD-NOT-APPEAR",
    "account_id": "acct-123"
  },
  "last_refresh": "2026-07-25T18:00:00.809105657Z"
}`, accessTokenExpiringAt(time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)))

func TestDecodeCredentialsAcceptsObservedSchema(t *testing.T) {
	got, err := DecodeCredentials([]byte(validAuthFile), "test")
	if err != nil {
		t.Fatalf("DecodeCredentials: %v", err)
	}
	if got.AccountID != "acct-123" {
		t.Errorf("AccountID = %q, want %q", got.AccountID, "acct-123")
	}
	want := time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)
	if !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v (from the access token's own exp claim)", got.ExpiresAt, want)
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
	doc := fmt.Sprintf(`{"tokens":{"access_token":%q,"future_field":1},"auth_mode":"apiKey"}`,
		accessTokenExpiringAt(time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)))
	if _, err := DecodeCredentials([]byte(doc), "test"); err != nil {
		t.Fatalf("unknown fields must not break decoding: %v", err)
	}
}

// A tokens.expires_at no observed auth.json carries is still honoured if a
// future CLI version writes one, and takes priority over the JWT claim.
func TestDecodeCredentialsPrefersAnExplicitExpiresAtOverTheJWTClaim(t *testing.T) {
	jwtExp := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) // deliberately implausible
	explicit := time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)
	doc := fmt.Sprintf(`{"tokens":{"access_token":%q,"expires_at":%q}}`,
		accessTokenExpiringAt(jwtExp), explicit.Format(time.RFC3339))

	got, err := DecodeCredentials([]byte(doc), "test")
	if err != nil {
		t.Fatalf("DecodeCredentials: %v", err)
	}
	if !got.ExpiresAt.Equal(explicit) {
		t.Errorf("ExpiresAt = %v, want the explicit field %v to win over the JWT claim", got.ExpiresAt, explicit)
	}
}

// An expires_at that is present but unparseable degrades to the JWT claim
// rather than failing outright: the field costs nothing to attempt first, and
// a malformed one is not evidence the fallback is unusable too.
func TestDecodeCredentialsFallsBackToTheJWTClaimWhenExpiresAtIsUnparseable(t *testing.T) {
	want := time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)
	doc := fmt.Sprintf(`{"tokens":{"access_token":%q,"expires_at":"not-a-timestamp"}}`, accessTokenExpiringAt(want))

	got, err := DecodeCredentials([]byte(doc), "test")
	if err != nil {
		t.Fatalf("DecodeCredentials: %v", err)
	}
	if !got.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v from the fallback", got.ExpiresAt, want)
	}
}

func TestDecodeCredentialsRejects(t *testing.T) {
	cases := map[string]struct {
		doc  string
		kind credential.FailureKind
	}{
		"not json":             {`not json`, credential.Malformed},
		"empty object":         {`{}`, credential.Incomplete},
		"missing access token": {`{"tokens":{"account_id":"a"}}`, credential.Incomplete},
		"empty access token":   {`{"tokens":{"access_token":""}}`, credential.Incomplete},
		"access token is not a JWT at all": {
			`{"tokens":{"access_token":"not-a-jwt"}}`, credential.Incomplete,
		},
		"access token JWT payload is not base64": {
			`{"tokens":{"access_token":"a.!!!not-base64!!!.c"}}`, credential.Incomplete,
		},
		"access token JWT carries no exp claim": {
			fmt.Sprintf(`{"tokens":{"access_token":%q}}`, makeJWT(`{"sub":"user"}`)), credential.Incomplete,
		},
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
	doc := fmt.Sprintf(`{"tokens":{"access_token":%q}}`,
		accessTokenExpiringAt(time.Date(2026, 7, 25, 23, 36, 19, 0, time.UTC)))
	got, err := DecodeCredentials([]byte(doc), "test")
	if err != nil {
		t.Fatalf("DecodeCredentials: %v", err)
	}
	if got.AccountID != "" {
		t.Errorf("AccountID = %q, want empty", got.AccountID)
	}
}

func TestJWTExpiryToleratesGarbage(t *testing.T) {
	for name, token := range map[string]string{
		"empty":            "",
		"one segment":      "abc",
		"two segments":     "abc.def",
		"four segments":    "a.b.c.d",
		"non-base64 body":  "a.!!!.c",
		"body is not json": makeJWT("not json"),
		"exp is zero":      makeJWT(`{"exp":0}`),
		"exp is negative":  makeJWT(`{"exp":-1}`),
		"exp is missing":   makeJWT(`{"sub":"user"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := jwtExpiry(token); ok {
				t.Errorf("jwtExpiry(%q) = ok, want false", token)
			}
		})
	}
}
