# meterAI

Go 1.25.5, module `github.com/larahfelipe/meterai`. Ships as a single Windows tray binary; developed and tested on Linux/WSL.
[README.md](README.md) carries the full design rationale — read it before changing behaviour in
`internal/credential`, `internal/poll`, or the provider contract.

## Commands

```sh
go test -short -race ./...     # offline, deterministic — the default loop
go test -race ./...            # also hits the live Anthropic endpoint (1 request/run)
go vet ./...
GOOS=windows go vet ./...      # REQUIRED before calling any change done
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/meterAI.exe ./cmd/meterai
go run ./cmd/meterai           # headless: prints each poll to stderr (see tray_other.go)
```

**A clean local run does not mean it compiles.** `singleton_windows.go`, `locate_windows.go` and
`tray_windows.go` are excluded from every Linux build, vet and test. `GOOS=windows go vet ./...` is
the only automated check they get: none of the three has a single test. WSL binfmt does execute
`GOOS=windows go test -c` binaries here, so Windows-tagged tests are possible — there just are none
yet, and that is the largest hole in the suite.

`CGO_ENABLED=0` is a requirement, not a convenience: it is what allows cross-compilation with no C
toolchain. Do not add a dependency that needs CGO.

## Layering

```
credential.Cache ─┬→ provider/anthropic → poll.Poller ─┬→ tray → i18n
                  └→ identity.Cache ───────────────────┘
                          ↘ internal/quota ↙
```

Dependencies flow one way and converge on `internal/quota`, which imports no project package —
changing it changes every layer above. `cmd/meterai/main.go` wires everything together.

`identity.Cache` takes the credential path from `credential.Cache` rather than resolving a home
directory, which is what keeps the account shown in the menu tied to the subscription being polled.
It is read at most once per credential path and every failure is non-fatal: the account rows hide and
polling continues.

Vendor-specific knowledge stops inside `internal/provider/<vendor>/`. Never name that directory
`vendor`: Go silently drops such a directory from build, vet and tests with no warning.

`internal/tray/format.go` and `internal/trayicon` are pure and fully testable; the `_windows.go` and
`_other.go` files are platform glue and are not.

**Menu row layout is a tab, not padding.** `tray.MenuRowTitle` puts the gauge after `\t`, which
Windows menus draw flush right; spaces cannot align a column in a proportional font. systray sets
items as `MFT_STRING` (no owner-draw), which is what makes the shell's own tab handling apply. A row
with no gauge carries no tab, because an empty right column widens every other row.

## Invariants that span files

**Never write to the credential file.** No OAuth flow, no refresh, no rewrite. Anthropic's
`refresh_token` rotates on use, so refreshing here would invalidate the copy the CLI holds and log
the user out of their CLI. Expiry degrades instead: `quota.Unauthorized` → `poll.DegradedInterval` →
a message telling the user to run `claude` once. `credential.Secret` redacts through `String`,
`GoString` and `MarshalJSON`, enforced by a test; `Reveal()` in the outbound `Authorization` header
is its only legitimate call site. Never put a secret on a command line, never retain one past its use.

**`FetchError.Kind` is the single input to both the retry policy (`poll.backoff`) and the
user-facing message (`tray.humanizeError`)**, so misclassifying one produces a wrong cadence *and* a
wrong instruction.

| Kind | Poller reaction |
|---|---|
| `Unauthorized`, `Protocol` | `DegradedInterval` — waiting cannot fix these |
| `RateLimited` | `max(Retry-After, interval)` |
| `Transient` | doubles per consecutive failure, capped at `MaxBackoff` |

`credential.FailureKind` is a parallel taxonomy: `Absent`/`Unreadable` mean "try the next candidate"
(`credential.IsAbsent`), the rest mean "stop and tell the user".

**The 300s poll floor is asserted in three places that behave differently:** `poll.New` and
`poll.Poller.SetInterval` silently raise a faster interval (callers may go slower, never faster),
while `config.Validate` rejects one with a message. Change all or none. `tray.IntervalPresets` never
offers a cadence below it, and a test asserts every preset survives `config.Validate`.

**A settings change is persisted before it is applied.** The tray derives a whole document through
`tray.WithInterval`/`WithLanguage`, which validate the entire config rather than the field, then
persists it through the `Wiring.SaveSettings` function — the tray never learns the config path. Only
after the write succeeds does the change reach the poller and the presenter, so the menu can never
show a state that is not on disk. A new cadence governs the delay computed after the next poll; a
timer already waiting is never cut short.

**`quota.MeterID` (`<vendor>:<kind>`) and `anthropic.VendorKey` are persisted keys** — stable across
releases even if the vendor renames its own field. `Snapshot.Product` is the opposite: display text
the provider owns ("Claude"), free to change, never a key. Product branding belongs in the provider
package, not in `internal/i18n`, because it is vendor knowledge and is identical in every language.

**Meter order in a `Snapshot` is load-bearing.** It sets menu row order and decides which meters
survive tooltip truncation at 127 runes and the fixed `maxMeterRows = 6` menu slots. Never sort it,
never build it by iterating a map.

**Every menu item is allocated once in `onReady` and only ever shown or hidden** — meter rows, the
account header and the `maxDetailRows` submenu rows — because systray can add an item but never
remove one. A row with nothing to say is hidden, never left blank, and a submenu with no visible rows
hides its parent too.

**Money never passes through `float64`** — `quota.Money` is minor units and `Exponent` is
presentation only. **`Percent` is not clamped at 100** in the model; only `trayicon.Render` clamps,
for display.

**`tray.Run` must own the main goroutine** on Windows: systray locks the OS thread holding the
message loop. The poller runs in a goroutine, and `updates` is a capacity-1 *signal* rather than a
queue — the UI re-reads `Controller.State()`, so a coalesced signal cannot show stale data.

**Time is injected, never called:** `Poller.now`/`Poller.after`, `Cache.now`,
`anthropic.Provider.now`, and explicit `now` parameters throughout `internal/tray`. Keep new code in
this shape or its tests become clock-dependent.

## Adding a provider

Implement `quota.Provider` under `internal/provider/<name>/`; the core does not change. README has
the full contract. The two things easiest to get wrong: take credentials through a local
`CredentialSource` interface so the package knows nothing about WSL or paths, and treat every remote
field as optional — these endpoints are undocumented, so an unrecognizable document must become
`quota.Protocol`, never a panic and never a zero value passing as valid.

## Conventions

- Every user-visible string resolves through `internal/i18n`. `en-US` is the default catalogue and
  `pt-BR` is opt-in through the config file; a string literal reaching the UI directly is a defect.
  Code, comments and identifiers are English.
- Comments carry invariants, security assumptions and non-obvious reasoning only. Match the existing
  density; no mechanical comments, no transitory notes.
- Named constants documented with the measurement or platform limit that justifies the value
  (`maxTooltipRunes`, `borderWidth`, `maxCredentialBytes`). No bare literals.
- Live tests are gated on `testing.Short()` (`live_e2e_test.go`, `smoke_test.go`). Schema drift is
  otherwise caught by the `internal/provider/anthropic` fixture, which reproduces the real response
  including its null internal-codename keys.
- No comment references a planning document or any other file that is expected to disappear.

## Engineering

Simplicity, readability and maintainability outrank cleverness. An abstraction earns its place by
having two concrete call sites or by being an interface at a layer boundary — nothing else. Keep
coupling low in the direction the layering already establishes: a package that acquires knowledge of
a vendor, of WSL, of Windows, or of a file format outside its own responsibility has been designed
wrong. New behaviour should slot into the existing seams (`quota.Provider`, `CredentialSource`,
injected clocks, the pure/glue split) rather than require them to be reshaped.

## Security

The credential invariants above are the floor, not the whole rule.

- No secret, token, path-derived identifier or account detail reaches a log line, an error message,
  a temporary file, or a command line — not even truncated.
- Every external input surface states where it is validated and what it does on rejection. Bound
  every read from a file or socket; an undocumented producer can always grow its output.
- **Never make a permanent change to the user's environment without explicit prior approval:**
  registry keys, startup entries, services, scheduled tasks, shortcuts, files outside this app's own
  config directory. When a feature needs one, stop, explain why, list the alternatives, and wait.
- Every file the app creates — config, cache, state — has a single documented purpose and is
  described in README.md. Nothing is written that no feature reads.

## Testing

Every behaviour ships with tests covering, where they apply: the happy path, boundaries (empty,
single, maximum, off-by-one), error handling, invalid input, regressions for defects already fixed,
and the cases that would corrupt state or leak data. Tests are deterministic, order-independent, and
share no mutable state; clocks, filesystem, and network are injected or stubbed. Assert observable
behaviour, never an implementation detail.
