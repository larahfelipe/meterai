package i18n

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/larahfelipe/meterai/internal/quota"
)

func TestParseResolvesSupportedTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		tag  string
		want Lang
	}{
		{"empty selects the default", "", DefaultLang},
		{"whitespace only selects the default", "   ", DefaultLang},
		{"exact english tag", "en-US", LangEnUS},
		{"exact portuguese tag", "pt-BR", LangPtBR},
		{"hand-typed lower case", "pt-br", LangPtBR},
		{"hand-typed upper case", "EN-US", LangEnUS},
		{"surrounding whitespace", "  pt-BR\t", LangPtBR},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.tag)
			if err != nil {
				t.Fatalf("Parse(%q) returned %v", tc.tag, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q) = %q, want %q", tc.tag, got, tc.want)
			}
		})
	}
}

func TestParseRejectsUnsupportedTag(t *testing.T) {
	for _, tag := range []string{"de-DE", "en", "pt", "klingon", "en_US"} {
		got, err := Parse(tag)
		if !errors.Is(err, ErrUnsupportedLang) {
			t.Fatalf("Parse(%q) error = %v, want ErrUnsupportedLang", tag, err)
		}
		if got != "" {
			t.Fatalf("Parse(%q) returned language %q alongside an error", tag, got)
		}
		// The user fixes this by editing the file by hand, so the message has to
		// name both the offending value and the accepted ones.
		if !strings.Contains(err.Error(), tag) {
			t.Errorf("Parse(%q) error %q does not name the rejected tag", tag, err)
		}
		for _, supported := range Available() {
			if !strings.Contains(err.Error(), string(supported)) {
				t.Errorf("Parse(%q) error %q does not list %q", tag, err, supported)
			}
		}
	}
}

func TestLangStringIsTheConfigTag(t *testing.T) {
	// The tag is what the config file persists, so it must round-trip through
	// String and Parse unchanged.
	for _, lang := range Available() {
		if got := lang.String(); got != string(lang) {
			t.Fatalf("Lang(%q).String() = %q", string(lang), got)
		}
		parsed, err := Parse(lang.String())
		if err != nil || parsed != lang {
			t.Fatalf("Parse(%q.String()) = %q, %v", lang, parsed, err)
		}
	}
}

func TestAvailableMatchesCataloguesAndIsStable(t *testing.T) {
	available := Available()
	if len(available) != len(catalogs) {
		t.Fatalf("Available() has %d entries, catalogs has %d", len(available), len(catalogs))
	}
	for _, lang := range available {
		if _, ok := catalogs[lang]; !ok {
			t.Errorf("Available() offers %q with no catalogue", lang)
		}
	}
	if first, second := Available(), Available(); !slices.Equal(first, second) {
		t.Errorf("Available() is not order-stable: %v then %v", first, second)
	}
	if available[0] != DefaultLang {
		t.Errorf("Available()[0] = %q, want the default %q first", available[0], DefaultLang)
	}
}

func TestEveryCatalogueDefinesEveryKey(t *testing.T) {
	for key := Key(1); key < keyCount; key++ {
		if keyNames[key] == "" {
			t.Errorf("Key(%d) has no name in keyNames", uint8(key))
		}
		for lang, catalog := range catalogs {
			message, ok := catalog.messages[key]
			if !ok {
				t.Errorf("catalogue %q is missing key %s", lang, key)
				continue
			}
			if strings.TrimSpace(message) == "" {
				t.Errorf("catalogue %q renders key %s as blank", lang, key)
			}
			// Callers join messages with an explicit separator, so a message that
			// carries its own padding produces doubled spacing.
			if message != strings.TrimSpace(message) {
				t.Errorf("catalogue %q key %s has leading or trailing whitespace: %q", lang, key, message)
			}
		}
	}
}

func TestNoCatalogueDefinesUnknownKey(t *testing.T) {
	for lang, catalog := range catalogs {
		for key := range catalog.messages {
			if key == 0 || key >= keyCount {
				t.Errorf("catalogue %q defines out-of-range key %d", lang, uint8(key))
			}
		}
	}
}

// A translation that drops or reorders a format verb only fails at runtime, and
// only in the language nobody develops in, so the signatures are compared here.
func TestFormatVerbsMatchAcrossCatalogues(t *testing.T) {
	reference := catalogs[DefaultLang]
	for key := Key(1); key < keyCount; key++ {
		want := formatVerbs(reference.messages[key])
		for lang, catalog := range catalogs {
			if got := formatVerbs(catalog.messages[key]); !slices.Equal(got, want) {
				t.Errorf("catalogue %q key %s has verbs %v, want %v (from %q)", lang, key, got, want, DefaultLang)
			}
		}
	}
}

func TestMeterLabelsCoverEveryCatalogue(t *testing.T) {
	reference := catalogs[DefaultLang]
	for lang, catalog := range catalogs {
		if len(catalog.meterLabels) != len(reference.meterLabels) {
			t.Errorf("catalogue %q defines %d meter labels, want %d", lang, len(catalog.meterLabels), len(reference.meterLabels))
		}
		for id := range reference.meterLabels {
			label, ok := catalog.meterLabels[id]
			if !ok {
				t.Errorf("catalogue %q is missing a label for meter %q", lang, id)
				continue
			}
			if strings.TrimSpace(label) == "" {
				t.Errorf("catalogue %q renders meter %q as blank", lang, id)
			}
		}
	}
}

func TestForFallsBackToDefaultCatalogue(t *testing.T) {
	if got := For(LangPtBR); got.Lang() != LangPtBR {
		t.Fatalf("For(pt-BR).Lang() = %q", got.Lang())
	}
	for _, lang := range []Lang{"de-DE", "", "EN-US"} {
		if got := For(lang); got.Lang() != DefaultLang {
			t.Errorf("For(%q).Lang() = %q, want the default %q", lang, got.Lang(), DefaultLang)
		}
	}
}

func TestTextRendersArguments(t *testing.T) {
	if got, want := For(LangEnUS).Text(CountdownMinutes, 42), "42m"; got != want {
		t.Errorf("en-US CountdownMinutes = %q, want %q", got, want)
	}
	if got, want := For(LangPtBR).Text(CountdownMinutes, 42), "42min"; got != want {
		t.Errorf("pt-BR CountdownMinutes = %q, want %q", got, want)
	}
	if got, want := For(LangEnUS).Text(CountdownHours, 3, 7), "3h07"; got != want {
		t.Errorf("en-US CountdownHours = %q, want %q", got, want)
	}
}

func TestTextWithoutArgumentsIsVerbatim(t *testing.T) {
	// A translation containing a literal percent sign must survive untouched;
	// routing it through Sprintf would turn it into a formatting artefact.
	literal := "100% consumed"
	catalog := &Catalog{lang: "test", messages: map[Key]string{StatusFirstPoll: literal}}
	if got := catalog.Text(StatusFirstPoll); got != literal {
		t.Errorf("Text() = %q, want the message verbatim %q", got, literal)
	}
}

func TestTextMakesAMissingKeyVisible(t *testing.T) {
	catalog := &Catalog{lang: "test", messages: map[Key]string{}}
	got := catalog.Text(MenuQuit)
	if got != "!MenuQuit" {
		t.Errorf("Text(MenuQuit) = %q, want %q", got, "!MenuQuit")
	}
	if strings.TrimSpace(got) == "" {
		t.Error("a missing key rendered as blank, which is indistinguishable from a UI bug")
	}
}

func TestKeyStringNamesInvalidKeys(t *testing.T) {
	for _, tc := range []struct {
		key  Key
		want string
	}{
		{MenuRefresh, "MenuRefresh"},
		{0, "Key(0)"},
		{keyCount, "Key(" + itoa(uint8(keyCount)) + ")"},
		{255, "Key(255)"},
	} {
		if got := tc.key.String(); got != tc.want {
			t.Errorf("Key(%d).String() = %q, want %q", uint8(tc.key), got, tc.want)
		}
	}
}

func TestMeterLabelFallsBackToProviderName(t *testing.T) {
	catalog := For(LangPtBR)
	if got, want := catalog.MeterLabel("anthropic:session", "session"), "Sessão (5h)"; got != want {
		t.Errorf("MeterLabel(anthropic:session) = %q, want %q", got, want)
	}
	// A window the vendor adds after this release must still reach the UI.
	if got, want := catalog.MeterLabel("anthropic:monthly_opus", "monthly_opus"), "monthly_opus"; got != want {
		t.Errorf("MeterLabel(unknown) = %q, want the provider name %q", got, want)
	}
	if got := catalog.MeterLabel(quota.MeterID(""), ""); got != "" {
		t.Errorf("MeterLabel(empty) = %q, want an empty string", got)
	}
}

// formatVerbs reduces a message to the sequence of fmt verbs it consumes, so
// two translations can be compared without regard to wording or order of words.
// Escaped "%%" consumes no argument and is skipped.
func formatVerbs(message string) []string {
	const formatFlagsAndWidth = "+-# 0123456789.*"
	var verbs []string
	runes := []rune(message)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '%' {
			continue
		}
		i++
		if i >= len(runes) || runes[i] == '%' {
			continue
		}
		for i < len(runes) && strings.ContainsRune(formatFlagsAndWidth, runes[i]) {
			i++
		}
		if i < len(runes) {
			verbs = append(verbs, string(runes[i]))
		}
	}
	return verbs
}

func itoa(v uint8) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
