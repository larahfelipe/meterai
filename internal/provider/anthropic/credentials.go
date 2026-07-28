package anthropic

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
)

// credentialRelPath locates the Claude CLI's credential file relative to a
// home directory, native or reached over WSL. The literal directory and file
// names are this package's own knowledge; internal/credential never sees them.
var credentialRelPath = credential.RelPath{Dir: ".claude", File: ".credentials.json"}

// NewCredentialCache builds the credential source this vendor's provider reads
// from. configuredPath is the path pinned in the settings document, or empty
// for autodiscovery.
//
// The relative path and the decoder describe one CLI's file and one CLI's
// on-disk schema; they are paired here rather than at the call site because
// crossing one vendor's path with another's decoder type-checks, and the result
// is an app that silently monitors nothing.
func NewCredentialCache(configuredPath string) *credential.Cache {
	return credential.NewCache(configuredPath, credentialRelPath, DecodeCredentials, credential.DefaultSkewMargin)
}

// credentialDocument records the on-disk schema, which the CLI publishes
// nowhere and which was established by inspection. It decodes only the three
// fields this app uses: the same object also carries refreshToken,
// refreshTokenExpiresAt, scopes, rateLimitTier and a sibling
// organizationUuid, and naming them here would materialize them — the refresh
// token above all — as live strings in this process for no purpose the app
// has. expiresAt is epoch MILLISECONDS.
type credentialDocument struct {
	ClaudeAiOauth struct {
		AccessToken      string `json:"accessToken"`
		ExpiresAtMillis  int64  `json:"expiresAt"`
		SubscriptionType string `json:"subscriptionType"`
	} `json:"claudeAiOauth"`
}

// DecodeCredentials is this vendor's credential.Decoder.
func DecodeCredentials(raw []byte, path string) (*credential.Credentials, error) {
	var doc credentialDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &credential.Failure{Kind: credential.Malformed, Path: path, Err: err}
	}
	o := doc.ClaudeAiOauth
	switch {
	case o.AccessToken == "":
		return nil, &credential.Failure{Kind: credential.Incomplete, Path: path, Err: errors.New("claudeAiOauth.accessToken is empty")}
	case o.ExpiresAtMillis <= 0:
		return nil, &credential.Failure{Kind: credential.Incomplete, Path: path, Err: errors.New("claudeAiOauth.expiresAt is not a positive epoch-millisecond value")}
	}
	return &credential.Credentials{
		AccessToken:      credential.Secret(o.AccessToken),
		ExpiresAt:        time.UnixMilli(o.ExpiresAtMillis).UTC(),
		SubscriptionType: o.SubscriptionType,
		Source:           path,
	}, nil
}
