// Package poll drives a quota.Provider on a schedule and publishes the result.
// The schedule is not fixed: the delay after each poll is derived from the kind
// of failure that poll produced, because the endpoints being polled are
// undocumented and an aggressive retry against a 429 is the one behaviour most
// likely to get a subscription flagged.
package poll

import (
	"context"
	"errors"
	"sync"
	"time"

	"meterAI/internal/quota"
)

const (
	// DefaultInterval is the steady-state cadence. The endpoints publish no
	// RateLimit-* headers, so there is no server-side signal to tune against
	// and shortening this is guesswork with an account-level downside.
	DefaultInterval = 300 * time.Second

	// MaxBackoff caps exponential backoff. Beyond this, waiting longer stops
	// buying reliability and only delays recovery.
	MaxBackoff = 30 * time.Minute

	// DegradedInterval applies to failures no retry can clear: an expired
	// credential needs the user to run their CLI, and a schema change needs a
	// new release.
	DegradedInterval = 15 * time.Minute

	// ManualRefreshFloor is the minimum spacing between user-triggered polls,
	// so that clicking refresh repeatedly cannot outpace the automatic cadence.
	ManualRefreshFloor = 60 * time.Second
)

// State is the complete observable state of the poller. It is a value type:
// consumers receive a copy and cannot mutate the poller through it.
type State struct {
	// Snapshot is the most recent successful observation, or nil before the
	// first success. It is deliberately retained across failures so the UI can
	// keep showing the last known figures while degraded.
	Snapshot *quota.Snapshot
	// UpdatedAt is when Snapshot was observed, not when the last poll ran.
	UpdatedAt  time.Time
	LastError  error
	NextPollAt time.Time
	// ConsecutiveFailures drives backoff and is reset by any success.
	ConsecutiveFailures int
}

// IsStale reports whether the displayed snapshot predates a failed poll, which
// the UI must disclose so an outdated percentage is never read as current.
func (s State) IsStale() bool { return s.LastError != nil && s.Snapshot != nil }

// Poller polls one provider. Exactly one goroutine may call Run; State and
// Refresh are safe to call concurrently from any goroutine.
//
// INV: mu guards state and lastPollAt.
type Poller struct {
	provider quota.Provider
	interval time.Duration

	// now and after are injected so scheduling is deterministic under test.
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
	// onUpdate is invoked after every poll, outside the lock. It must not
	// block: the UI adapter is expected to hand off to its own event loop.
	onUpdate func(State)

	mu         sync.RWMutex
	state      State
	lastPollAt time.Time

	// refresh has capacity 1 so a burst of manual requests coalesces into one
	// poll instead of queueing.
	refresh chan struct{}
}

func discardState(State) {}

// New builds a Poller. An interval below DefaultInterval is raised to it:
// callers may configure a slower cadence, never a faster one. onUpdate may be
// nil, in which case the state is still available through State.
func New(provider quota.Provider, interval time.Duration, onUpdate func(State)) *Poller {
	if interval < DefaultInterval {
		interval = DefaultInterval
	}
	if onUpdate == nil {
		onUpdate = discardState
	}
	return &Poller{
		provider: provider,
		interval: interval,
		now:      time.Now,
		after:    time.After,
		onUpdate: onUpdate,
		refresh:  make(chan struct{}, 1),
	}
}

func (p *Poller) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// Refresh requests an immediate poll. It reports false when the request was
// rejected because the previous poll is more recent than ManualRefreshFloor,
// which lets the UI say so instead of appearing to do nothing.
func (p *Poller) Refresh() bool {
	now := p.now()

	p.mu.Lock()
	if !p.lastPollAt.IsZero() && now.Sub(p.lastPollAt) < ManualRefreshFloor {
		p.mu.Unlock()
		return false
	}
	// Charge the floor at acceptance rather than at completion: a queued but
	// unexecuted refresh still counts as "a poll is coming", so a burst of
	// clicks cannot slip several requests through the window before the first
	// one lands.
	p.lastPollAt = now
	p.mu.Unlock()

	select {
	case p.refresh <- struct{}{}:
	default:
	}
	return true
}

// Run polls until ctx is cancelled. It polls once immediately so the UI has
// data without waiting a full interval.
func (p *Poller) Run(ctx context.Context) {
	for {
		timer := p.after(p.pollOnce(ctx))
		select {
		case <-ctx.Done():
			return
		case <-timer:
		case <-p.refresh:
		}
	}
}

// pollOnce performs one poll, publishes the resulting state, and returns the
// delay before the next automatic poll.
func (p *Poller) pollOnce(ctx context.Context) time.Duration {
	snapshot, err := p.provider.Fetch(ctx)
	now := p.now()

	p.mu.Lock()
	p.lastPollAt = now
	if err == nil {
		p.state.Snapshot = snapshot
		p.state.UpdatedAt = now
		p.state.LastError = nil
		p.state.ConsecutiveFailures = 0
	} else {
		p.state.LastError = err
		p.state.ConsecutiveFailures++
	}
	delay := backoff(err, p.state.ConsecutiveFailures, p.interval)
	p.state.NextPollAt = now.Add(delay)
	published := p.state
	p.mu.Unlock()

	p.onUpdate(published)
	return delay
}

// backoff derives the next delay from the failure kind: only genuine transient
// faults escalate, since retrying the others sooner cannot help.
func backoff(err error, consecutiveFailures int, interval time.Duration) time.Duration {
	if err == nil {
		return interval
	}
	var fe *quota.FetchError
	if errors.As(err, &fe) {
		switch fe.Kind {
		case quota.RateLimited:
			// Honour the vendor's own instruction, but never poll faster than
			// the steady-state cadence.
			if fe.RetryAfter > interval {
				return fe.RetryAfter
			}
			return interval
		case quota.Unauthorized, quota.Protocol:
			return DegradedInterval
		}
	}
	// The cap in the loop condition bounds the doubling, so an unbounded
	// failure count cannot overflow the duration.
	delay := interval
	for i := 1; i < consecutiveFailures && delay < MaxBackoff; i++ {
		delay *= 2
	}
	if delay > MaxBackoff {
		return MaxBackoff
	}
	return delay
}
