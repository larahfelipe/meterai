package tray

import (
	"errors"
	"strings"
	"testing"
	"time"

	"meterAI/internal/config"
	"meterAI/internal/poll"
	"meterAI/internal/quota"
)

var now = time.Date(2026, 7, 25, 18, 0, 0, 0, time.UTC)

// liveState reproduces the shape of a real Anthropic poll: an inactive 5-hour
// session window plus the active weekly window, with the figures observed on
// 2026-07-25.
func liveState() poll.State {
	return poll.State{
		Snapshot: &quota.Snapshot{
			Vendor: "anthropic",
			Plan:   "pro",
			Meters: []quota.Meter{
				&quota.Utilization{
					MeterID: "anthropic:session", Name: "Sessão (5h)", Percent: 23,
					Reset: now.Add(2*time.Hour + 54*time.Minute), Level: quota.SeverityNormal,
				},
				&quota.Utilization{
					MeterID: "anthropic:weekly_all", Name: "Semanal", Percent: 74,
					Reset: now.Add(25*time.Hour + 44*time.Minute), Level: quota.SeverityNormal,
					IsActive: true,
				},
			},
		},
		UpdatedAt:  now.Add(-90 * time.Second),
		NextPollAt: now.Add(3*time.Minute + 30*time.Second),
	}
}

func TestRowsRenderMetersInProviderOrder(t *testing.T) {
	rows := Rows(liveState(), now)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Label != "Sessão (5h)" || rows[0].Detail != "23% · reset em 2h54" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Label != "Semanal" || rows[1].Detail != "74% · reset em 1d01h" {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

func TestRowsBeforeFirstPoll(t *testing.T) {
	if rows := Rows(poll.State{}, now); rows != nil {
		t.Errorf("rows = %v, want nil before the first successful poll", rows)
	}
}

func TestRowsRenderMoneyMeters(t *testing.T) {
	limit := quota.Money{AmountMinor: 5000, Currency: "USD", Exponent: 2}
	state := poll.State{Snapshot: &quota.Snapshot{Meters: []quota.Meter{
		&quota.Balance{
			MeterID: "anthropic:spend", Name: "Créditos",
			Used:  quota.Money{AmountMinor: 1234, Currency: "USD", Exponent: 2},
			Limit: &limit, Percent: 24.68,
		},
		&quota.Balance{
			MeterID: "openrouter:credits", Name: "Saldo",
			Used: quota.Money{AmountMinor: 750, Currency: "USD", Exponent: 2},
		},
	}}}
	rows := Rows(state, now)
	if rows[0].Detail != "12.34 USD de 50.00 USD" {
		t.Errorf("capped balance = %q", rows[0].Detail)
	}
	// An uncapped balance must not invent a limit or a percentage.
	if rows[1].Detail != "7.50 USD" {
		t.Errorf("uncapped balance = %q", rows[1].Detail)
	}
}

func TestFormatCountdown(t *testing.T) {
	cases := map[time.Duration]string{
		-time.Hour:                      "agora",
		0:                               "agora",
		30 * time.Second:                "<1min",
		time.Minute:                     "1min",
		59*time.Minute + 59*time.Second: "59min",
		time.Hour:                       "1h00",
		2*time.Hour + 54*time.Minute:    "2h54",
		23*time.Hour + 59*time.Minute:   "23h59",
		24 * time.Hour:                  "1d00h",
		25*time.Hour + 44*time.Minute:   "1d01h",
		7*24*time.Hour + 3*time.Hour:    "7d03h",
	}
	for d, want := range cases {
		if got := formatCountdown(d); got != want {
			t.Errorf("formatCountdown(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestStatusTextByHealth(t *testing.T) {
	healthy := StatusText(liveState(), now)
	if healthy != "Atualizado há 1min · próxima em 3min" {
		t.Errorf("healthy status = %q", healthy)
	}

	if got := StatusText(poll.State{}, now); got != "Consultando…" {
		t.Errorf("startup status = %q", got)
	}
}

func TestStatusTextNamesTheUserAction(t *testing.T) {
	cases := map[quota.FetchErrorKind]string{
		quota.Unauthorized: "rode `claude`",
		quota.RateLimited:  "aguardando",
		quota.Transient:    "tentando novamente",
		quota.Protocol:     "atualizado",
	}
	for kind, want := range cases {
		state := liveState()
		state.LastError = &quota.FetchError{Kind: kind, Err: errors.New("x")}
		got := StatusText(state, now)
		if !strings.Contains(got, want) {
			t.Errorf("status for %v = %q, does not mention %q", kind, got, want)
		}
		// A stale reading must say how old it is, so the user never reads an
		// outdated percentage as current.
		if !strings.Contains(got, "dados de 1min atrás") {
			t.Errorf("status for %v = %q, does not disclose staleness", kind, got)
		}
	}
}

func TestStatusTextForUntypedError(t *testing.T) {
	state := poll.State{LastError: errors.New("boom")}
	got := StatusText(state, now)
	if !strings.Contains(got, "Falha inesperada") {
		t.Errorf("status = %q", got)
	}
	// With no snapshot there is nothing to call stale.
	if strings.Contains(got, "dados de") {
		t.Errorf("status = %q, must not claim stale data when none was ever fetched", got)
	}
}

func TestTooltipFitsThePlatformLimit(t *testing.T) {
	tooltip := Tooltip(liveState(), now)
	if lines := strings.Split(tooltip, "\n"); len(lines) != 3 {
		t.Errorf("tooltip = %q, want two meter lines plus a status line", tooltip)
	}
	if !strings.Contains(tooltip, "Sessão (5h)  23%") {
		t.Errorf("tooltip = %q", tooltip)
	}

	// A vendor returning many meters must not produce a tooltip Windows will
	// reject or cut mid-character.
	crowded := liveState()
	for i := 0; i < 20; i++ {
		crowded.Snapshot.Meters = append(crowded.Snapshot.Meters, &quota.Utilization{
			Name: "Janela adicional de nome muito comprido", Percent: 50, Reset: now.Add(time.Hour),
		})
	}
	long := Tooltip(crowded, now)
	if runes := []rune(long); len(runes) > maxTooltipRunes {
		t.Errorf("tooltip is %d runes, over the %d limit", len(runes), maxTooltipRunes)
	}
	if !strings.HasSuffix(long, ellipsis) {
		t.Errorf("a truncated tooltip must be marked: %q", long)
	}
	if !strings.ContainsRune(long, 'ã') {
		t.Error("truncation corrupted multi-byte characters")
	}
}

func TestIconStateTracksTheActiveWindow(t *testing.T) {
	cfg := config.Default() // warn 75, critical 90
	percent, level, stale := IconState(liveState(), cfg)
	// The weekly window is the active one, even though it is not first.
	if percent != 74 {
		t.Errorf("percent = %v, want the active window's 74", percent)
	}
	if level != quota.SeverityNormal {
		t.Errorf("level = %v, want normal just under the 75%% threshold", level)
	}
	if stale {
		t.Error("a healthy state must not render as stale")
	}
}

func TestIconStateEscalatesOnLocalThreshold(t *testing.T) {
	cfg := config.Default()
	state := liveState()
	state.Snapshot.Meters[1].(*quota.Utilization).Percent = 91

	_, level, _ := IconState(state, cfg)
	if level != quota.SeverityCritical {
		t.Errorf("level = %v, want critical past the local threshold even though the vendor said normal", level)
	}
}

func TestIconStateBeforeFirstPoll(t *testing.T) {
	percent, level, stale := IconState(poll.State{}, config.Default())
	if percent != 0 || level != quota.SeverityNormal || !stale {
		t.Errorf("startup icon = (%v,%v,%v), want an empty greyed gauge", percent, level, stale)
	}
}

func TestIconStateWithAnEmptySnapshot(t *testing.T) {
	state := poll.State{Snapshot: &quota.Snapshot{Vendor: "anthropic"}}
	if _, _, stale := IconState(state, config.Default()); !stale {
		t.Error("a snapshot carrying no meters must render as stale, not as 0% healthy")
	}
}

func TestIconStateMarksStaleOnFailure(t *testing.T) {
	state := liveState()
	state.LastError = &quota.FetchError{Kind: quota.Transient, Err: errors.New("offline")}
	if _, _, stale := IconState(state, config.Default()); !stale {
		t.Error("a failed poll must grey the icon")
	}
}
