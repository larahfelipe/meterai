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

// testRel stands in for a vendor's RelPath in every test in this package: the
// discovery and caching logic under test does not care which vendor it is.
var testRel = RelPath{Dir: ".testvendor", File: "creds.json"}

// testDocument is a minimal, vendor-neutral fixture shape: {"token":...,
// "expiresAtMillis":...}. decodeTestDocument is the Decoder every test in this
// package injects, standing in for a real vendor's Decoder without pulling one
// package in from another.
func testDocument(token string, expiresAtMillis int64) []byte {
	return []byte(fmt.Sprintf(`{"token":%q,"expiresAtMillis":%d}`, token, expiresAtMillis))
}

func decodeTestDocument(raw []byte, path string) (*Credentials, error) {
	var doc struct {
		Token           string `json:"token"`
		ExpiresAtMillis int64  `json:"expiresAtMillis"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &Failure{Kind: Malformed, Path: path, Err: err}
	}
	switch {
	case doc.Token == "":
		return nil, &Failure{Kind: Incomplete, Path: path, Err: errors.New("token is empty")}
	case doc.ExpiresAtMillis <= 0:
		return nil, &Failure{Kind: Incomplete, Path: path, Err: errors.New("expiresAtMillis is not positive")}
	}
	return &Credentials{
		AccessToken: Secret(doc.Token),
		ExpiresAt:   time.UnixMilli(doc.ExpiresAtMillis).UTC(),
		Source:      path,
	}, nil
}

func TestSecretRedaction(t *testing.T) {
	const token = "sk-ant-oat01-SHOULD-NOT-APPEAR"
	s := Secret(token)

	if got := s.String(); got != redactedPlaceholder {
		t.Errorf("String() = %q, want %q", got, redactedPlaceholder)
	}
	if got := fmt.Sprintf("%#v", s); got != redactedPlaceholder {
		t.Errorf("%%#v (GoString) = %q, want %q", got, redactedPlaceholder)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var decoded string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("MarshalJSON produced invalid JSON %s: %v", encoded, err)
	}
	if decoded != redactedPlaceholder {
		t.Errorf("MarshalJSON = %s, decodes to %q, want %q", encoded, decoded, redactedPlaceholder)
	}
	if got := s.Reveal(); got != token {
		t.Errorf("Reveal() = %q, want the raw token", got)
	}
}

func TestSecretNeverRenders(t *testing.T) {
	const token = "sk-ant-oat01-LIVE-TOKEN"
	c, err := decodeTestDocument(testDocument(token, 1785022579691), "test")
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

func TestFailureKindStringAllValues(t *testing.T) {
	cases := map[FailureKind]string{
		Absent:          "absent",
		Unreadable:      "unreadable",
		Malformed:       "malformed",
		Incomplete:      "incomplete",
		Expired:         "expired",
		FailureKind(0):  "unknown",
		FailureKind(99): "unknown",
	}
	for kind, want := range cases {
		if got := kind.String(); got != want {
			t.Errorf("FailureKind(%d).String() = %q, want %q", kind, got, want)
		}
	}
}

func TestFailureErrorMessage(t *testing.T) {
	cause := errors.New("permission denied")
	f := &Failure{Kind: Unreadable, Path: "/tmp/creds.json", Err: cause}

	want := `credential unreadable at "/tmp/creds.json": permission denied`
	if got := f.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(f, cause) {
		t.Error("Failure does not unwrap to its cause, breaking errors.Is chains")
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
	_, err := Load(filepath.Join(t.TempDir(), "nothing-here.json"), decodeTestDocument)
	if !IsAbsent(err) {
		t.Fatalf("missing file must be skippable, got %v", err)
	}
}

func TestLoadBoundsFileSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), testRel.File)
	oversized := make([]byte, maxCredentialBytes+1)
	for i := range oversized {
		oversized[i] = ' '
	}
	if err := os.WriteFile(path, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, decodeTestDocument)
	var f *Failure
	if !errors.As(err, &f) || f.Kind != Malformed {
		t.Fatalf("oversized file must be rejected as malformed, got %v", err)
	}
}

func TestLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), testRel.File)
	if err := os.WriteFile(path, testDocument("TOKEN", 1785022579691), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, decodeTestDocument)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Source != path {
		t.Errorf("Source = %q, want %q", got.Source, path)
	}
}

func TestLoadPropagatesDecodeFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), testRel.File)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, decodeTestDocument)
	var f *Failure
	if !errors.As(err, &f) || f.Kind != Malformed {
		t.Fatalf("Load = %v, want the decoder's Malformed failure surfaced verbatim", err)
	}
}
