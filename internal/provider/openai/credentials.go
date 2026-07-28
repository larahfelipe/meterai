package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/larahfelipe/meterai/internal/credential"
)

// credentialRelPath locates the Codex CLI's credential file relative to a
// home directory, native or reached over WSL. The literal directory and file
// names are this package's own knowledge; internal/credential never sees them.
var credentialRelPath = credential.RelPath{Dir: ".codex", File: "auth.json"}

// NewCredentialCache builds the credential source this vendor's provider reads
// from, pairing this CLI's file location with this CLI's on-disk schema for the
// reason anthropic.NewCredentialCache states.
func NewCredentialCache(configuredPath string) *credential.Cache {
	return credential.NewCache(configuredPath, credentialRelPath, DecodeCredentials, credential.DefaultSkewMargin)
}

// authFile records the on-disk schema, established by inspection (the file
// carries no version marker). It decodes only the three fields this app uses:
// tokens.refresh_token and tokens.id_token also exist in the file but are
// never named here, mirroring anthropic.credentialDocument's rationale for
// omitting Claude's refreshToken — decoding either would materialize a
// long-lived secret (refresh_token) or a JWT whose claims this app has no use
// for (id_token) in this process for no purpose it has.
type authFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
		ExpiresAt   string `json:"expires_at"`
	} `json:"tokens"`
}

// DecodeCredentials is this vendor's credential.Decoder. A missing or
// unparseable expires_at is Incomplete — fail closed rather than trust an
// unbounded token or fall back to decoding a JWT exp claim to work around it.
func DecodeCredentials(raw []byte, path string) (*credential.Credentials, error) {
	var doc authFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &credential.Failure{Kind: credential.Malformed, Path: path, Err: err}
	}
	if doc.Tokens.AccessToken == "" {
		return nil, &credential.Failure{Kind: credential.Incomplete, Path: path, Err: errors.New("tokens.access_token is empty")}
	}
	if doc.Tokens.ExpiresAt == "" {
		return nil, &credential.Failure{Kind: credential.Incomplete, Path: path, Err: errors.New("tokens.expires_at is empty")}
	}
	expiresAt, err := time.Parse(time.RFC3339, doc.Tokens.ExpiresAt)
	if err != nil {
		return nil, &credential.Failure{Kind: credential.Incomplete, Path: path,
			Err: fmt.Errorf("tokens.expires_at is not RFC3339: %w", err)}
	}
	return &credential.Credentials{
		AccessToken: credential.Secret(doc.Tokens.AccessToken),
		ExpiresAt:   expiresAt.UTC(),
		AccountID:   doc.Tokens.AccountID,
		Source:      path,
	}, nil
}
