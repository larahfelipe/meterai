package poll

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/larahfelipe/meterai/internal/quota"
)

// scriptedProvider returns a queued result per call; the final entry repeats
// once the script is exhausted.
type scriptedProvider struct {
	mu      sync.Mutex
	script  []result
	callNum int
}

type result struct {
	snapshot *quota.Snapshot
	err      error
}

func (s *scriptedProvider) Vendor() string { return "test" }

func (s *scriptedProvider) Fetch(context.Context) (*quota.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.callNum
	if i >= len(s.script) {
		i = len(s.script) - 1
	}
	s.callNum++
	return s.script[i].snapshot, s.script[i].err
}

func (s *scriptedProvider) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callNum
}

// harness drives the poller's clock and timer deterministically: delays
// records every scheduled delay, and tick releases the current wait.
type harness struct {
	delays chan time.Duration
	tick   chan time.Time
	states chan State
	clock  time.Time
	mu     sync.Mutex
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func newHarness(t *testing.T, provider quota.Provider, interval time.Duration) (*Poller, *harness, context.CancelFunc) {
	t.Helper()
	h := &harness{
		delays: make(chan time.Duration, 32),
		tick:   make(chan time.Time),
		states: make(chan State, 32),
		clock:  time.Date(2026, 7, 25, 18, 0, 0, 0, time.UTC),
	}
	p := New(provider, interval, func(s State) { h.states <- s })
	p.now = h.now
	p.after = func(d time.Duration) <-chan time.Time {
		h.delays <- d
		return h.tick
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		// Release a poller parked on the timer so Run can observe ctx.Done.
		select {
		case h.tick <- time.Time{}:
		case <-done:
		case <-time.After(time.Second):
			t.Error("Run did not exit after cancellation")
		}
		<-done
	})
	return p, h, cancel
}

// nextCycle waits for one completed poll and returns the published state plus
// the delay scheduled after it.
func (h *harness) nextCycle(t *testing.T) (State, time.Duration) {
	t.Helper()
	select {
	case s := <-h.states:
		select {
		case d := <-h.delays:
			return s, d
		case <-time.After(2 * time.Second):
			t.Fatal("poller did not schedule a next poll")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not publish a state")
	}
	return State{}, 0
}

func okSnapshot(percent float64) *quota.Snapshot {
	return &quota.Snapshot{
		Vendor: "test",
		Meters: []quota.Meter{&quota.Utilization{
			MeterID: "test:session", Name: "Session", Percent: percent,
			Level: quota.SeverityNormal, IsActive: true,
		}},
	}
}

func TestPollerPublishesFirstResultWithoutWaiting(t *testing.T) {
	provider := &scriptedProvider{script: []result{{snapshot: okSnapshot(23)}}}
	_, h, _ := newHarness(t, provider, DefaultInterval)

	state, delay := h.nextCycle(t)
	if state.Snapshot == nil || state.Snapshot.Primary().(*quota.Utilization).Percent != 23 {
		t.Fatalf("first poll did not publish the snapshot: %+v", state)
	}
	if delay != DefaultInterval {
		t.Errorf("delay after success = %v, want %v", delay, DefaultInterval)
	}
	if !state.NextPollAt.Equal(h.now().Add(DefaultInterval)) {
		t.Errorf("NextPollAt = %v", state.NextPollAt)
	}
	if state.IsStale() {
		t.Errorf("healthy state misreported as stale: %+v", state)
	}
}

func TestPollerBacksOffExponentiallyOnTransientFailure(t *testing.T) {
	transient := &quota.FetchError{Kind: quota.Transient, Vendor: "test", Err: errors.New("dial timeout")}
	provider := &scriptedProvider{script: []result{{err: transient}}}
	_, h, _ := newHarness(t, provider, DefaultInterval)

	// 5m, 10m, 20m, then the cap: 8x the interval is 40m, past MaxBackoff.
	want := []time.Duration{
		DefaultInterval,
		2 * DefaultInterval,
		4 * DefaultInterval,
		MaxBackoff,
		MaxBackoff,
		MaxBackoff,
	}
	for i, wantDelay := range want {
		state, delay := h.nextCycle(t)
		if delay != wantDelay {
			t.Fatalf("failure %d: delay = %v, want %v", i+1, delay, wantDelay)
		}
		if state.ConsecutiveFailures != i+1 {
			t.Errorf("failure %d: ConsecutiveFailures = %d", i+1, state.ConsecutiveFailures)
		}
		h.tick <- time.Time{}
	}
}

// Backoff exists to poll less often, never more. A cadence the user configured
// past MaxBackoff is a slower one, so the cap must not pull it back down: an
// hourly poller that starts failing must not begin polling every 30 minutes.
func TestBackoffNeverPollsFasterThanTheConfiguredCadence(t *testing.T) {
	transient := &quota.FetchError{Kind: quota.Transient, Vendor: "test", Err: errors.New("dial timeout")}
	const slower = 2 * MaxBackoff

	for failures := 1; failures <= 8; failures++ {
		if got := backoff(transient, failures, slower); got < slower {
			t.Errorf("failure %d: delay = %v, want at least the configured %v", failures, got, slower)
		}
	}
	if got := backoff(transient, 8, slower); got != slower {
		t.Errorf("delay = %v, want the cadence itself as the ceiling", got)
	}
	// A rate-limit instruction shorter than the cadence is already floored at it.
	limited := &quota.FetchError{Kind: quota.RateLimited, Vendor: "test", RetryAfter: time.Second, Err: errors.New("429")}
	if got := backoff(limited, 1, slower); got != slower {
		t.Errorf("rate-limited delay = %v, want the configured %v", got, slower)
	}
}

func TestPollerResetsBackoffAfterRecovery(t *testing.T) {
	transient := &quota.FetchError{Kind: quota.Transient, Vendor: "test", Err: errors.New("dial timeout")}
	provider := &scriptedProvider{script: []result{
		{err: transient}, {err: transient}, {snapshot: okSnapshot(7)}, {err: transient},
	}}
	_, h, _ := newHarness(t, provider, DefaultInterval)

	if _, d := h.nextCycle(t); d != DefaultInterval {
		t.Fatalf("first failure delay = %v", d)
	}
	h.tick <- time.Time{}
	if _, d := h.nextCycle(t); d != 2*DefaultInterval {
		t.Fatalf("second failure delay = %v", d)
	}
	h.tick <- time.Time{}

	state, delay := h.nextCycle(t)
	if state.LastError != nil || state.ConsecutiveFailures != 0 {
		t.Errorf("success did not clear the failure state: %+v", state)
	}
	if delay != DefaultInterval {
		t.Errorf("delay after recovery = %v, want %v", delay, DefaultInterval)
	}
	h.tick <- time.Time{}

	// The next failure must restart from the base interval, not resume the
	// previous escalation.
	if _, d := h.nextCycle(t); d != DefaultInterval {
		t.Errorf("backoff resumed after a success: %v", d)
	}
}

func TestPollerKeepsLastSnapshotWhileDegraded(t *testing.T) {
	provider := &scriptedProvider{script: []result{
		{snapshot: okSnapshot(42)},
		{err: &quota.FetchError{Kind: quota.Transient, Vendor: "test", Err: errors.New("offline")}},
	}}
	_, h, _ := newHarness(t, provider, DefaultInterval)

	h.nextCycle(t)
	h.tick <- time.Time{}
	state, _ := h.nextCycle(t)

	if state.Snapshot == nil {
		t.Fatal("a failed poll discarded the last good snapshot")
	}
	if got := state.Snapshot.Primary().(*quota.Utilization).Percent; got != 42 {
		t.Errorf("stale snapshot = %v%%, want 42%%", got)
	}
	if !state.IsStale() {
		t.Error("IsStale must be true when showing a snapshot older than the last poll")
	}
}

func TestPollerScheduleByFailureKind(t *testing.T) {
	cases := map[string]struct {
		err  error
		want time.Duration
	}{
		"rate limited honours Retry-After": {
			err:  &quota.FetchError{Kind: quota.RateLimited, RetryAfter: 20 * time.Minute, Err: errors.New("429")},
			want: 20 * time.Minute,
		},
		"rate limited never polls faster than the cadence": {
			err:  &quota.FetchError{Kind: quota.RateLimited, RetryAfter: 5 * time.Second, Err: errors.New("429")},
			want: DefaultInterval,
		},
		"expired credential waits for user action": {
			err:  &quota.FetchError{Kind: quota.Unauthorized, Err: errors.New("401")},
			want: DegradedInterval,
		},
		"schema change waits for a fix": {
			err:  &quota.FetchError{Kind: quota.Protocol, Err: errors.New("unparseable")},
			want: DegradedInterval,
		},
		"untyped error is treated as transient": {
			err:  errors.New("bare error"),
			want: DefaultInterval,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			provider := &scriptedProvider{script: []result{{err: tc.err}}}
			_, h, _ := newHarness(t, provider, DefaultInterval)
			_, delay := h.nextCycle(t)
			if delay != tc.want {
				t.Errorf("delay = %v, want %v", delay, tc.want)
			}
		})
	}
}

func TestRefreshIsRateLimitedByFloor(t *testing.T) {
	provider := &scriptedProvider{script: []result{{snapshot: okSnapshot(7)}}}
	p, h, _ := newHarness(t, provider, DefaultInterval)
	h.nextCycle(t)

	if p.Refresh() {
		t.Error("a refresh immediately after a poll must be rejected")
	}
	h.advance(ManualRefreshFloor - time.Second)
	if p.Refresh() {
		t.Error("a refresh inside the floor must be rejected")
	}
	h.advance(2 * time.Second)
	if !p.Refresh() {
		t.Fatal("a refresh past the floor must be accepted")
	}

	state, _ := h.nextCycle(t)
	if state.Snapshot == nil {
		t.Fatal("accepted refresh did not produce a poll")
	}
	if provider.calls() != 2 {
		t.Errorf("provider calls = %d, want 2", provider.calls())
	}
}

func TestRefreshBurstCollapsesToOnePoll(t *testing.T) {
	provider := &scriptedProvider{script: []result{{snapshot: okSnapshot(7)}}}
	p, h, _ := newHarness(t, provider, DefaultInterval)
	h.nextCycle(t)

	h.advance(2 * ManualRefreshFloor)
	if !p.Refresh() {
		t.Fatal("the first refresh past the floor must be accepted")
	}
	// The floor is charged at acceptance, so the rest of the burst is refused
	// without ever reaching the poller. Without that, several requests slip
	// through in the window before the first poll updates lastPollAt.
	for i := 1; i < 8; i++ {
		if p.Refresh() {
			t.Fatalf("refresh %d was accepted inside the floor", i)
		}
	}

	h.nextCycle(t)
	if got := provider.calls(); got != 2 {
		t.Errorf("provider calls = %d, want 2 (burst did not coalesce)", got)
	}
}

func TestNewRaisesTooFastInterval(t *testing.T) {
	p := New(&scriptedProvider{script: []result{{snapshot: okSnapshot(1)}}}, time.Second, nil)
	if p.interval != DefaultInterval {
		t.Errorf("interval = %v, want %v: a faster-than-default cadence must be refused", p.interval, DefaultInterval)
	}
	slower := New(&scriptedProvider{script: []result{{snapshot: okSnapshot(1)}}}, time.Hour, nil)
	if slower.interval != time.Hour {
		t.Errorf("interval = %v, want 1h: a slower cadence is the caller's choice", slower.interval)
	}
}

func TestStateIsSafeUnderConcurrentReaders(t *testing.T) {
	provider := &scriptedProvider{script: []result{{snapshot: okSnapshot(50)}}}
	p, h, _ := newHarness(t, provider, DefaultInterval)
	h.nextCycle(t)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.State()
			_ = p.Refresh()
		}()
	}
	wg.Wait()
}

func TestSetIntervalGovernsTheDelayAfterTheNextPoll(t *testing.T) {
	provider := &scriptedProvider{script: []result{{snapshot: okSnapshot(10)}}}
	poller, h, _ := newHarness(t, provider, 10*time.Minute)

	if _, delay := h.nextCycle(t); delay != 10*time.Minute {
		t.Fatalf("first delay = %v, want the configured cadence", delay)
	}

	poller.SetInterval(20 * time.Minute)
	if got := poller.Interval(); got != 20*time.Minute {
		t.Fatalf("Interval() = %v, want the new cadence", got)
	}

	// The waiting timer is not cut short; the new cadence governs the delay
	// computed after the poll that timer releases.
	h.tick <- time.Time{}
	if _, delay := h.nextCycle(t); delay != 20*time.Minute {
		t.Errorf("delay after the change = %v, want 20m", delay)
	}
}

func TestSetIntervalEnforcesTheSameFloorAsNew(t *testing.T) {
	// The floor protects an undocumented endpoint; a settings change is not a way
	// around it.
	provider := &scriptedProvider{script: []result{{snapshot: okSnapshot(10)}}}
	poller, _, _ := newHarness(t, provider, time.Hour)

	for _, tooFast := range []time.Duration{0, -time.Minute, time.Second, DefaultInterval - time.Nanosecond} {
		poller.SetInterval(tooFast)
		if got := poller.Interval(); got != DefaultInterval {
			t.Errorf("SetInterval(%v) left the cadence at %v, want the %v floor", tooFast, got, DefaultInterval)
		}
	}
	poller.SetInterval(DefaultInterval)
	if got := poller.Interval(); got != DefaultInterval {
		t.Errorf("SetInterval at exactly the floor = %v", got)
	}
}

func TestSetIntervalIsSafeAlongsideARunningPoller(t *testing.T) {
	provider := &scriptedProvider{script: []result{{snapshot: okSnapshot(10)}}}
	poller, h, _ := newHarness(t, provider, DefaultInterval)
	h.nextCycle(t)

	// The UI goroutine changes the cadence while the poller reads it.
	var wg sync.WaitGroup
	wg.Add(2)
	for worker := 0; worker < 2; worker++ {
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				poller.SetInterval(time.Duration(10+worker) * time.Minute)
				_ = poller.Interval()
				_ = poller.State()
			}
		}(worker)
	}
	for i := 0; i < 3; i++ {
		h.tick <- time.Time{}
		h.nextCycle(t)
	}
	wg.Wait()
}
