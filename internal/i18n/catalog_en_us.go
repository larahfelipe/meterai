package i18n

import "github.com/larahfelipe/meterai/internal/quota"

// enUS is the default catalogue and the reference for every other language: a
// key absent here is absent from the app, and the completeness test compares
// every other catalogue against the key enum, not against this table.
var enUS = &Catalog{
	lang: LangEnUS,
	messages: map[Key]string{
		MenuRefresh:          "Refresh now",
		MenuRefreshTooltip:   "Forces an immediate poll",
		MenuQuit:             "Quit",
		MenuQuitTooltip:      "Stops the monitor",
		RefreshRejected:      "Polled too recently — wait a moment",
		MeterResetSuffix:     "· resets in %s",
		BalanceUsedOfLimit:   "%s of %s",
		CountdownNow:         "now",
		CountdownUnderMinute: "<1m",
		CountdownMinutes:     "%dm",
		CountdownHours:       "%dh%02d",
		CountdownDays:        "%dd%02dh",
		StatusFirstPoll:      "Polling…",
		StatusUpdated:        "Updated %s ago · next in %s",
		StatusStale:          "(data %s old)",
		ErrorUnauthorized:    "Credential expired — run `claude` once to renew it",
		ErrorRateLimited:     "Request limit reached — waiting",
		ErrorTransient:       "No response from the API — retrying",
		ErrorProtocol:        "The API changed shape — the app needs an update",
		ErrorUnrecognized:    "Polling failed",
		ErrorUnexpected:      "Unexpected failure while polling",
	},
	// The keys are the MeterIDs the providers mint and persist; they are matched
	// literally here so this package never imports a provider.
	meterLabels: map[quota.MeterID]string{
		"anthropic:session":       "Session (5h)",
		"anthropic:weekly_all":    "Weekly",
		"anthropic:weekly_opus":   "Weekly (Opus)",
		"anthropic:weekly_sonnet": "Weekly (Sonnet)",
		"anthropic:weekly_cowork": "Weekly (Cowork)",
		"anthropic:spend":         "Credits",
	},
}
