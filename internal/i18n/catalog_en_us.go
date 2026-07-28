package i18n

import "github.com/larahfelipe/meterai/internal/quota"

// enUS is the default catalogue and the reference for every other language: a
// key absent here is absent from the app, and the completeness test compares
// every other catalogue against the key enum, not against this table.
var enUS = &Catalog{
	lang: LangEnUS,
	messages: map[Key]string{
		MenuRefresh:   "Refresh now",
		MenuProviders: "Providers",
		MenuSettings:  "Settings",
		// Sentence case, like every other multi-word caption in the menu. The
		// two thresholds name the level they open rather than the reading they
		// compare against, because the reading is in the value column beside them.
		MenuUsageAlerts:       "Usage alerts",
		MenuWarnThreshold:     "Warning threshold",
		MenuCriticalThreshold: "Critical threshold",
		MenuInterval:          "Update every",
		MenuLanguage:          "Language",
		IntervalMinutes:       "%d min",
		IntervalHours:         "%d h",
		SettingsSaveFailed:    "Settings could not be saved — check the config file",
		AccountName:           "Account",
		AccountEmail:          "E-mail",
		AccountOrganization:   "Organization",
		// The configured value is interpolated title-cased, so the qualifier around
		// it carries the same case: the row is one label, not a sentence, and
		// "High" alone names nothing.
		EffortLevel:          "%s Effort",
		MenuQuit:             "Quit",
		RefreshRejected:      "Polled too recently — wait a moment",
		MeterResetSuffix:     "· resets in %s",
		BalanceUsedOfLimit:   "%s of %s",
		CountdownNow:         "moments",
		CountdownUnderMinute: "<1m",
		CountdownMinutes:     "%dm",
		CountdownHours:       "%dh%02d",
		CountdownDays:        "%dd%02dh",
		StatusFirstPoll:      "Polling…",
		StatusUpdated:        "Updated %s ago · next in %s",
		// StatusUpdatedBrief is the same fact inside the tooltip, where the whole
		// text has 63 characters to work with and the cadence is one setting away.
		StatusUpdatedBrief: "Updated %s ago",
		StatusStale:        "(data from %s ago)",
		ErrorUnauthorized:  "Credential expired — run `%s` once to renew it",
		ErrorRateLimited:   "Request limit reached — waiting",
		ErrorTransient:     "No response from the API — retrying",
		ErrorProtocol:      "The API changed shape — the app needs an update",
		ErrorUnrecognized:  "Polling failed",
		ErrorUnexpected:    "Unexpected failure while polling",
	},
	// The keys are the MeterIDs the providers mint and persist; they are matched
	// literally here so this package never imports a provider.
	meterLabels: map[quota.MeterID]string{
		"anthropic:session":          "Session (5h)",
		"anthropic:weekly_all":       "Weekly (7d)",
		"anthropic:weekly_opus":      "Weekly Opus (7d)",
		"anthropic:weekly_sonnet":    "Weekly Sonnet (7d)",
		"anthropic:weekly_cowork":    "Weekly Cowork (7d)",
		"anthropic:spend":            "Credits",
		"openai:session":             "Session (5h)",
		"openai:weekly":              "Weekly (7d)",
		"openai:code_review_session": "Code Review (5h)",
		"openai:code_review_weekly":  "Code Review Weekly (7d)",
		"openai:credits":             "Credits",
	},
}
