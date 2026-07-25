# meterAI

Go 1.25.5, module `meterAI`. Ships as a single Windows tray binary; developed and tested on Linux/WSL.
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
credential.Cache → provider/anthropic → poll.Poller → tray
                        ↘ internal/quota ↙
```

Dependencies flow one way and converge on `internal/quota`, which imports no project package —
changing it changes every layer above. `cmd/meterai/main.go` wires everything together.

Vendor-specific knowledge stops inside `internal/provider/<vendor>/`. Never name that directory
`vendor`: Go silently drops such a directory from build, vet and tests with no warning.

`internal/tray/format.go` and `internal/trayicon` are pure and fully testable; the `_windows.go` and
`_other.go` files are platform glue and are not.

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

**The 300s poll floor is asserted in two places that behave differently:** `poll.New` silently raises
a faster interval (callers may go slower, never faster), while `config.Validate` rejects one with a
message. Change both or neither.

**`quota.MeterID` (`<vendor>:<kind>`) and `anthropic.VendorKey` are persisted keys** — stable across
releases even if the vendor renames its own field.

**Meter order in a `Snapshot` is load-bearing.** It sets menu row order and decides which meters
survive tooltip truncation at 127 runes and the fixed `maxMeterRows = 6` menu slots, since systray
can add items but never remove them. Never sort it, never build it by iterating a map.

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

- User-visible strings are Brazilian Portuguese (menu items, status lines, `anthropic.windowLabels`).
  Code, comments and identifiers are English.
- Comments carry invariants, security assumptions and non-obvious reasoning only. Match the existing
  density; no mechanical comments, no transitory notes.
- Named constants documented with the measurement or platform limit that justifies the value
  (`maxTooltipRunes`, `borderWidth`, `maxCredentialBytes`). No bare literals.
- Live tests are gated on `testing.Short()` (`live_e2e_test.go`, `smoke_test.go`). Schema drift is
  otherwise caught by the `internal/provider/anthropic` fixture, which reproduces the real response
  including its null internal-codename keys.
