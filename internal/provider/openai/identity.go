package openai

import "github.com/larahfelipe/meterai/internal/identity"

// NoIdentity implements the tray.CLIReader shape (Account, Preferences,
// Invalidate) for a vendor with nothing local to read.
//
// The Codex CLI keeps no account-profile document comparable to Claude's
// .claude.json: the closest thing is the id_token JWT inside auth.json, and
// decoding it to show a display name would materialize a bearer-adjacent
// credential in this process for a cosmetic row — exactly what
// credential.Credentials already declines to do for Claude's refresh token
// (see credentials.go). This is therefore a permanent gap, not one this type
// will grow out of.
//
// A model/effort row is not implemented either: it would come from Codex's
// local config.toml, and no verified schema for that file's model or
// reasoning-effort keys was available at the time this package was written
// (neither ai-usagebar nor codexbar reads it). Closing this one is tracked
// separately from the account gap above — it needs a confirmed schema, not a
// change of policy.
type NoIdentity struct{}

func (NoIdentity) Account() (*identity.Account, error) { return nil, identity.ErrNoAccount }
func (NoIdentity) Preferences() (*identity.Preferences, error) {
	return nil, identity.ErrNoPreferences
}
func (NoIdentity) Invalidate() {}
