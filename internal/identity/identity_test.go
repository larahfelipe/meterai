package identity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// observedDocument reproduces the shape of a real state document: the account
// object surrounded by the CLI's own bookkeeping, with the null values and the
// keys this package deliberately ignores.
const observedDocument = `{
  "numStartups": 10,
  "installMethod": "native",
  "migrationVersion": 13,
  "oauthAccount": {
    "accountUuid": "00000000-0000-4000-8000-000000000001",
    "displayName": "Sample",
    "emailAddress": "sample@example.com",
    "organizationName": "Sample Organization Workspace",
    "organizationUuid": "00000000-0000-4000-8000-000000000002",
    "organizationRole": "admin",
    "workspaceRole": null,
    "seatTier": null,
    "billingType": "stripe_subscription",
    "profileFetchedAt": 1785082515505
  },
  "projects": {"/home/user/project": {"lastCost": 0.08}},
  "tipsHistory": {}
}`

func TestParseReadsTheObservedDocument(t *testing.T) {
	account, err := Parse([]byte(observedDocument))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if account.DisplayName != "Sample" {
		t.Errorf("DisplayName = %q", account.DisplayName)
	}
	if account.Email != "sample@example.com" {
		t.Errorf("Email = %q", account.Email)
	}
	if account.Organization != "Sample Organization Workspace" {
		t.Errorf("Organization = %q", account.Organization)
	}
}

func TestParseAcceptsAPartialAccount(t *testing.T) {
	// The CLI writes what its profile response carried; a personal account has
	// no organization, and that must not invalidate the rest.
	account, err := Parse([]byte(`{"oauthAccount":{"emailAddress":"only@example.com"}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if account.Email != "only@example.com" {
		t.Errorf("Email = %q", account.Email)
	}
	if account.DisplayName != "" || account.Organization != "" {
		t.Errorf("absent fields were invented: %+v", account)
	}
}

func TestParseTrimsSurroundingWhitespace(t *testing.T) {
	// A stray space would misalign the menu row it is rendered into.
	account, err := Parse([]byte(`{"oauthAccount":{"displayName":"  Sample  ","emailAddress":"\tsample@example.com\n"}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if account.DisplayName != "Sample" || account.Email != "sample@example.com" {
		t.Errorf("account = %+v", account)
	}
}

func TestParseReportsNoAccount(t *testing.T) {
	for name, document := range map[string]string{
		"key absent":        `{"numStartups":1}`,
		"explicitly null":   `{"oauthAccount":null}`,
		"object empty":      `{"oauthAccount":{}}`,
		"fields empty":      `{"oauthAccount":{"displayName":"","emailAddress":"","organizationName":""}}`,
		"fields whitespace": `{"oauthAccount":{"displayName":"   ","emailAddress":"\t"}}`,
		"unrelated fields":  `{"oauthAccount":{"organizationRole":"admin","seatTier":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			account, err := Parse([]byte(document))
			if !errors.Is(err, ErrNoAccount) {
				t.Fatalf("Parse error = %v, want ErrNoAccount", err)
			}
			if account != nil {
				t.Errorf("Parse returned %+v alongside an error", account)
			}
		})
	}
}

func TestParseRejectsUnusableDocuments(t *testing.T) {
	for name, document := range map[string]string{
		"truncated":                 `{"oauthAccount":{"displayName":"Sample"`,
		"not an object":             `["oauthAccount"]`,
		"account is an array":       `{"oauthAccount":["Sample"]}`,
		"account is a string":       `{"oauthAccount":"Sample"}`,
		"field is a number":         `{"oauthAccount":{"emailAddress":42}}`,
		"field is an object":        `{"oauthAccount":{"displayName":{"first":"Sample"}}}`,
		"empty document":            ``,
		"whitespace only":           `   `,
		"schema replaced wholesale": `{"account":{"email":"sample@example.com"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			account, err := Parse([]byte(document))
			if err == nil {
				t.Fatalf("Parse accepted %q as %+v", document, account)
			}
			if account != nil {
				t.Errorf("Parse returned %+v alongside an error", account)
			}
		})
	}
}

// The document holds the user's project paths and account details; a decoding
// failure must describe the structure, never quote the contents.
func TestParseErrorsDoNotEchoTheDocument(t *testing.T) {
	const secretish = "sample@example.com"
	for _, document := range []string{
		`{"oauthAccount":{"emailAddress":` + secretish + `}}`,
		`{"oauthAccount":"` + secretish + `"}`,
		`{"oauthAccount":{"displayName":{"nested":"` + secretish + `"}}}`,
	} {
		_, err := Parse([]byte(document))
		if err == nil {
			t.Fatalf("Parse accepted %q", document)
		}
		if strings.Contains(err.Error(), secretish) {
			t.Errorf("error %q echoes document contents", err)
		}
	}
}

func TestParseIgnoresUnknownAndRenamedKeys(t *testing.T) {
	// The CLI has migrated this schema repeatedly; keys appearing beside the ones
	// read here must not disturb parsing.
	document := `{"oauthAccount":{"displayName":"Sample","futureField":{"deep":[1,2,3]}},"newTopLevel":true}`
	account, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if account.DisplayName != "Sample" {
		t.Errorf("DisplayName = %q", account.DisplayName)
	}
}

func TestStatePathForSitsBesideTheClaudeDirectory(t *testing.T) {
	credential := filepath.Join("/home", "user", ".claude", ".credentials.json")
	want := filepath.Join("/home", "user", stateFileName)

	got, err := StatePathFor(credential)
	if err != nil {
		t.Fatalf("StatePathFor: %v", err)
	}
	if got != want {
		t.Errorf("StatePathFor(%q) = %q, want %q", credential, got, want)
	}
}

func TestStatePathForFollowsAPinnedCredentialHome(t *testing.T) {
	// The credential may come from a distribution's home or a pinned path. The
	// state document must be looked for in that same home, so the account on
	// screen cannot belong to a different installation than the token polled.
	credential := filepath.Join("/mnt", "wsl", "distro", "home", "other", ".claude", ".credentials.json")
	want := filepath.Join("/mnt", "wsl", "distro", "home", "other", stateFileName)

	got, err := StatePathFor(credential)
	if err != nil {
		t.Fatalf("StatePathFor: %v", err)
	}
	if got != want {
		t.Errorf("StatePathFor(%q) = %q, want %q", credential, got, want)
	}
}

func TestStatePathForRejectsAnEmptyCredentialPath(t *testing.T) {
	for _, path := range []string{"", "   ", "\t"} {
		got, err := StatePathFor(path)
		if err == nil {
			t.Fatalf("StatePathFor(%q) = %q, want an error", path, got)
		}
		if got != "" {
			t.Errorf("StatePathFor(%q) returned %q alongside an error", path, got)
		}
	}
}

func TestLoadReadsAFileOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), stateFileName)
	if err := os.WriteFile(path, []byte(observedDocument), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	account, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if account.Email != "sample@example.com" {
		t.Errorf("Email = %q", account.Email)
	}
}

func TestLoadReportsAnAbsentFileAsNotExist(t *testing.T) {
	// The ordinary case on a machine where the CLI has never run: the caller
	// hides the account rows instead of treating this as a failure.
	_, err := Load(filepath.Join(t.TempDir(), stateFileName))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load error = %v, want os.ErrNotExist", err)
	}
}

func TestLoadRejectsADirectory(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load accepted a directory")
	}
}

func TestLoadBoundsTheRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), stateFileName)
	// One byte past the cap, with the account nested behind the padding so a
	// parser that ignored the limit would still succeed.
	padding := strings.Repeat("p", maxStateBytes)
	document, err := json.Marshal(map[string]any{
		"padding":      padding,
		"oauthAccount": map[string]string{"emailAddress": "sample@example.com"},
	})
	if err != nil {
		t.Fatalf("build oversized document: %v", err)
	}
	if len(document) <= maxStateBytes {
		t.Fatalf("fixture is %d bytes, not over the %d cap", len(document), maxStateBytes)
	}
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	account, err := Load(path)
	if err == nil {
		t.Fatalf("Load accepted an oversized document as %+v", account)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error %q does not name the limit it enforced", err)
	}
}

func TestLoadReportsNoAccountThroughTheWrappedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), stateFileName)
	if err := os.WriteFile(path, []byte(`{"numStartups":1}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrNoAccount) {
		t.Fatalf("Load error = %v, want ErrNoAccount to survive wrapping", err)
	}
}

// A decoder error this build does not recognise must still be reduced to a
// structural description: encoding/json may add error types, and the fallback is
// the only thing standing between a future one and a message quoting the file.
func TestJSONReasonWithholdsUnrecognizedErrors(t *testing.T) {
	const secretish = "sample@example.com"
	reason := jsonReason(errors.New("json: cannot decode " + secretish))
	if strings.Contains(reason, secretish) {
		t.Errorf("jsonReason = %q, echoes the underlying message", reason)
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("jsonReason returned nothing, leaving the failure undescribed")
	}
}
