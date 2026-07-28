package openai

import (
	"errors"
	"testing"

	"github.com/larahfelipe/meterai/internal/identity"
)

func TestNoIdentityReportsNoAccount(t *testing.T) {
	account, err := (NoIdentity{}).Account()
	if account != nil {
		t.Errorf("Account() = %+v, want nil", account)
	}
	if !errors.Is(err, identity.ErrNoAccount) {
		t.Errorf("err = %v, want identity.ErrNoAccount", err)
	}
}

func TestNoIdentityReportsNoPreferences(t *testing.T) {
	prefs, err := (NoIdentity{}).Preferences()
	if prefs != nil {
		t.Errorf("Preferences() = %+v, want nil", prefs)
	}
	if !errors.Is(err, identity.ErrNoPreferences) {
		t.Errorf("err = %v, want identity.ErrNoPreferences", err)
	}
}

func TestNoIdentityInvalidateIsSafeToCall(t *testing.T) {
	// Must not panic: the tray calls Invalidate() on every provider uniformly
	// from the manual-refresh path.
	(NoIdentity{}).Invalidate()
}
