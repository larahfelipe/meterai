package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
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

// tokens is the observed shape of auth.json's "tokens" object, confirmed
// against a live file. It carries no expires_at field at all — the field
// third-party inspection had suggested turned out not to exist in the
// version actually shipped. tokens.refresh_token and tokens.id_token also
// exist in the file but are never named here: decoding refresh_token would
// materialize a long-lived secret this app never uses, mirroring
// anthropic.credentialDocument's rationale for omitting Claude's
// refreshToken, and id_token is a second JWT this app has no use for even
// though access_token — decoded below for its own exp claim — happens to be
// one too.
type tokens struct {
	AccessToken string `json:"access_token"`
	AccountID   string `json:"account_id"`
	// ExpiresAt is not present in any observed auth.json, but costs nothing to
	// prefer if a future CLI version adds it directly: tokenExpiry checks it
	// before falling back to decoding access_token's own claim.
	ExpiresAt string `json:"expires_at"`
}

type authFile struct {
	Tokens tokens `json:"tokens"`
}

// DecodeCredentials is this vendor's credential.Decoder. An access token with
// no determinable expiry — neither tokens.expires_at nor a readable exp claim
// in the token itself — is Incomplete: fail closed rather than treat it as
// unboundedly valid.
func DecodeCredentials(raw []byte, path string) (*credential.Credentials, error) {
	var doc authFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, &credential.Failure{Kind: credential.Malformed, Path: path, Err: err}
	}
	if doc.Tokens.AccessToken == "" {
		return nil, &credential.Failure{Kind: credential.Incomplete, Path: path, Err: errors.New("tokens.access_token is empty")}
	}

	expiresAt, ok := tokenExpiry(doc.Tokens)
	if !ok {
		return nil, &credential.Failure{Kind: credential.Incomplete, Path: path,
			Err: errors.New("could not determine access token expiry: no tokens.expires_at and no readable exp claim in the token")}
	}

	return &credential.Credentials{
		AccessToken: credential.Secret(doc.Tokens.AccessToken),
		ExpiresAt:   expiresAt,
		AccountID:   doc.Tokens.AccountID,
		Source:      path,
	}, nil
}

// tokenExpiry prefers an explicit tokens.expires_at, then falls back to the
// exp claim already encoded in access_token itself.
func tokenExpiry(t tokens) (time.Time, bool) {
	if t.ExpiresAt != "" {
		if parsed, err := time.Parse(time.RFC3339, t.ExpiresAt); err == nil {
			return parsed.UTC(), true
		}
	}
	return jwtExpiry(t.AccessToken)
}

// jwtExpiry reads the exp (Unix seconds) claim out of a JWT's payload segment,
// without verifying its signature. That is not a gap: this app is not
// authenticating the token, only reading a timestamp the issuing server
// already put in it — the same instant the server itself will start
// rejecting the token at. It is also not the same decision as declining to
// read id_token above: access_token is the one secret this app already
// reveals in every request's Authorization header, so reading a non-secret
// claim already inside it materializes nothing that request does not already
// send.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := decodeJWTSegment(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0).UTC(), true
}

// decodeJWTSegment accepts both the unpadded base64url JWT libraries emit and
// the padded form, since nothing guarantees which one a given issuer writes.
func decodeJWTSegment(segment string) ([]byte, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(segment); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(segment)
}
