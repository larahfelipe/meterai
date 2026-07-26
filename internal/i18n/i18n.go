// Package i18n holds every user-visible string in the app, one catalogue per
// language. Nothing above it formats a literal: the tray asks for a Key and
// gets the active language's text, so adding a language is adding a table and
// never touching a call site.
//
// Meter labels are keyed by quota.MeterID rather than translated inside each
// provider. The MeterID is already the stable cross-release contract between a
// provider and the rest of the app, which makes it the one identifier a
// translator can rely on; the alternative would push locale awareness into
// every provider package.
package i18n

import (
	"errors"
	"fmt"
	"strings"

	"github.com/larahfelipe/meterai/internal/quota"
)

// Lang is a BCP 47 tag. The value is persisted in the config file, so these
// strings must stay stable across releases.
type Lang string

const (
	LangEnUS Lang = "en-US"
	LangPtBR Lang = "pt-BR"
)

func (l Lang) String() string { return string(l) }

// nativeNames are endonyms: a language is offered in its own words, so a user
// who cannot read the current interface can still find theirs. They are not
// catalogue entries because they are identical in every catalogue.
var nativeNames = map[Lang]string{
	LangEnUS: "English (US)",
	LangPtBR: "Português (BR)",
}

// NativeName is how this language names itself. It falls back to the tag, which
// is still recognizable, rather than to an empty menu entry.
func (l Lang) NativeName() string {
	if name, ok := nativeNames[l]; ok {
		return name
	}
	return string(l)
}

// DefaultLang is the language used when the config file carries none.
const DefaultLang = LangEnUS

// ErrUnsupportedLang is returned by Parse for a tag with no catalogue, so the
// caller can distinguish a misconfiguration from any other failure.
var ErrUnsupportedLang = errors.New("unsupported language tag")

// Available lists the languages with a catalogue, in the order a settings menu
// should offer them. It is a fixed slice rather than the catalogue map's keys
// because map iteration order would reshuffle the menu between runs.
func Available() []Lang { return []Lang{LangEnUS, LangPtBR} }

// Parse resolves a config value. An empty tag selects DefaultLang, so a config
// document written before this field existed still loads. Matching is
// case-insensitive because the value is typed by hand.
func Parse(tag string) (Lang, error) {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return DefaultLang, nil
	}
	for _, candidate := range Available() {
		if strings.EqualFold(trimmed, string(candidate)) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%q: %w (supported: %s)", tag, ErrUnsupportedLang, strings.Join(tagList(), ", "))
}

func tagList() []string {
	tags := make([]string, 0, len(Available()))
	for _, l := range Available() {
		tags = append(tags, string(l))
	}
	return tags
}

// Key identifies one message. It is an enum rather than a string so a typo is a
// compile error instead of a blank menu entry at runtime.
type Key uint8

const (
	MenuRefresh Key = iota + 1
	MenuRefreshTooltip
	MenuDetails
	MenuDetailsTooltip
	MenuSettings
	MenuSettingsTooltip
	MenuInterval
	MenuIntervalTooltip
	MenuLanguage
	MenuLanguageTooltip
	IntervalMinutes
	IntervalHours
	SettingsSaveFailed
	AccountName
	AccountEmail
	AccountOrganization
	MenuQuit
	MenuQuitTooltip
	RefreshRejected
	MeterResetSuffix
	BalanceUsedOfLimit
	CountdownNow
	CountdownUnderMinute
	CountdownMinutes
	CountdownHours
	CountdownDays
	StatusFirstPoll
	StatusUpdated
	StatusStale
	ErrorUnauthorized
	ErrorRateLimited
	ErrorTransient
	ErrorProtocol
	ErrorUnrecognized
	ErrorUnexpected

	// keyCount is one past the last valid Key. It bounds the name table and
	// lets the completeness test enumerate every key without a hand-kept list.
	keyCount
)

var keyNames = [keyCount]string{
	MenuRefresh:          "MenuRefresh",
	MenuRefreshTooltip:   "MenuRefreshTooltip",
	MenuDetails:          "MenuDetails",
	MenuDetailsTooltip:   "MenuDetailsTooltip",
	MenuSettings:         "MenuSettings",
	MenuSettingsTooltip:  "MenuSettingsTooltip",
	MenuInterval:         "MenuInterval",
	MenuIntervalTooltip:  "MenuIntervalTooltip",
	MenuLanguage:         "MenuLanguage",
	MenuLanguageTooltip:  "MenuLanguageTooltip",
	IntervalMinutes:      "IntervalMinutes",
	IntervalHours:        "IntervalHours",
	SettingsSaveFailed:   "SettingsSaveFailed",
	AccountName:          "AccountName",
	AccountEmail:         "AccountEmail",
	AccountOrganization:  "AccountOrganization",
	MenuQuit:             "MenuQuit",
	MenuQuitTooltip:      "MenuQuitTooltip",
	RefreshRejected:      "RefreshRejected",
	MeterResetSuffix:     "MeterResetSuffix",
	BalanceUsedOfLimit:   "BalanceUsedOfLimit",
	CountdownNow:         "CountdownNow",
	CountdownUnderMinute: "CountdownUnderMinute",
	CountdownMinutes:     "CountdownMinutes",
	CountdownHours:       "CountdownHours",
	CountdownDays:        "CountdownDays",
	StatusFirstPoll:      "StatusFirstPoll",
	StatusUpdated:        "StatusUpdated",
	StatusStale:          "StatusStale",
	ErrorUnauthorized:    "ErrorUnauthorized",
	ErrorRateLimited:     "ErrorRateLimited",
	ErrorTransient:       "ErrorTransient",
	ErrorProtocol:        "ErrorProtocol",
	ErrorUnrecognized:    "ErrorUnrecognized",
	ErrorUnexpected:      "ErrorUnexpected",
}

func (k Key) String() string {
	if k == 0 || k >= keyCount || keyNames[k] == "" {
		return fmt.Sprintf("Key(%d)", uint8(k))
	}
	return keyNames[k]
}

// Catalog is one language's complete text. Instances are immutable after
// package initialization and therefore safe to share across goroutines.
type Catalog struct {
	lang        Lang
	messages    map[Key]string
	meterLabels map[quota.MeterID]string
}

var catalogs = map[Lang]*Catalog{
	LangEnUS: enUS,
	LangPtBR: ptBR,
}

// For returns the catalogue for lang, falling back to DefaultLang when there is
// none. The fallback keeps the UI legible if a language is ever removed while a
// config file still names it; Parse is what rejects an unknown tag.
func For(lang Lang) *Catalog {
	if catalog, ok := catalogs[lang]; ok {
		return catalog
	}
	return catalogs[DefaultLang]
}

func (c *Catalog) Lang() Lang { return c.lang }

// Text renders a message. Missing keys render as "!KeyName" instead of an empty
// string: a gap in a catalogue has to be visible to whoever sees the UI, and an
// empty menu row looks like a bug in the poller instead.
//
// With no arguments the message is returned verbatim, so a literal percent sign
// in a translation cannot turn into a formatting artefact.
func (c *Catalog) Text(key Key, args ...any) string {
	message, ok := c.messages[key]
	if !ok {
		return "!" + key.String()
	}
	if len(args) == 0 {
		return message
	}
	return fmt.Sprintf(message, args...)
}

// MeterLabel translates a meter's display name. fallback is the provider's own
// name for the meter and is returned untranslated for a kind this release has
// never seen, so a window the vendor adds tomorrow still appears in the UI.
func (c *Catalog) MeterLabel(id quota.MeterID, fallback string) string {
	if label, ok := c.meterLabels[id]; ok {
		return label
	}
	return fallback
}
