# meterAI — engineering guide

Windows notification-area monitor for AI subscription usage quotas. Go 1.25.5, module
`github.com/larahfelipe/meterai`. One binary, no CGO. Ships for Windows; developed, tested and
cross-compiled on Linux/WSL.

This file is the contract for changing the code: architecture, invariants, conventions and the bar a
change has to clear. [README.md](README.md) is the user-facing document — what the app does, how to
build it, how to configure it — and states the footprint promises in §3.1 to users. When a change
makes either document wrong, fix it in the same commit.

---

## 1. Commands

```sh
go test -short -race ./...     # offline, deterministic — the default loop
go test -race ./...            # also hits the live Anthropic endpoint (1 request/run)
go vet ./...
GOOS=windows go vet ./...      # REQUIRED before calling any change done
gofmt -l .                     # must print nothing

CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/meterAI.exe ./cmd/meterai

go run ./cmd/meterai           # headless: prints each poll to stderr (tray_other.go)

# the Windows build's own tests, executed here through WSL binfmt
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -o /tmp/cred.exe ./internal/credential && /tmp/cred.exe
```

**A clean local run does not prove the shipping build compiles.** `singleton_windows.go`,
`locate_windows.go` and `tray_windows.go` are excluded from every Linux build, vet and test, so
`GOOS=windows go vet ./...` is not optional. WSL binfmt executes `GOOS=windows go test -c` binaries
here, which is how `locate_windows_test.go` — the only Windows-tagged test in the project — runs.
`singleton_windows.go` and `tray_windows.go` have no tests at all; that is the largest known hole in
the suite, and anything moved out of them into pure code closes part of it.

`CGO_ENABLED=0` is a requirement, not a convenience: it is what allows cross-compilation with no C
toolchain. Never add a dependency that needs CGO.

---

## 2. Architecture

### 2.1 Layering

```
credential.Cache ─┬→ provider/anthropic → poll.Poller ─┬→ tray → i18n
                  └→ identity.Cache ───────────────────┘
                          ↘ internal/quota ↙
```

Dependencies flow one way and converge on `internal/quota`, which imports no project package —
changing it changes every layer above it. `cmd/meterai/main.go` is the only place that knows how the
pieces connect; nothing below it learns a file path, a vendor or a platform it does not own.

### 2.2 Packages

| Package | Owns | Must not know about |
|---|---|---|
| `internal/quota` | the vendor-neutral model, the fetch-error taxonomy, the `Provider` interface | anything in this project |
| `internal/credential` | locating, parsing and caching the CLI credential file; WSL enumeration | vendors, HTTP, the UI |
| `internal/provider/<vendor>` | one vendor's endpoint and response shape | WSL, paths, the config file, the UI |
| `internal/poll` | cadence, backoff, published state | vendors, the UI |
| `internal/identity` | the two CLI-owned documents (account, preferences) | vendors, the UI |
| `internal/config` | the settings document and its validation | the UI, providers |
| `internal/i18n` | every user-visible string, one catalogue per language | providers (it keys on `MeterID`) |
| `internal/tray` | presentation and the Windows menu | how any value was obtained |
| `internal/trayicon` | the ICO the notification area draws | any project package other than `internal/quota` |

Vendor-specific knowledge stops inside `internal/provider/<vendor>/`. Never name that directory
`vendor`: Go silently drops such a directory from build, vet and tests with no warning.

### 2.3 Pure code and platform glue

`internal/tray/format.go`, `internal/tray/bar.go`, `internal/tray/settings.go` and
`internal/trayicon` are pure and fully tested on any host. `_windows.go` and `_other.go`/`_unix.go`
files are glue: they wire widgets and syscalls and are not testable here. Every decision that
produces visible text or pixels belongs on the pure side.

### 2.4 Outside the graph

`internal/buildinfo` is a leaf holding the product name and version, so the outbound User-Agent and
the tray's accessible name cannot claim two different things. The binary carries no Windows version
resource and no embedded manifest: Task Manager and the property sheet show it by file name alone,
and it runs as invoker because that is the platform default for an unmanifested executable, not
because anything declares it.

---

## 3. Invariants that span files

### 3.1 The footprint is a ceiling, not a default

This app handles someone's credentials, so what it touches has to stay small enough to state in one
paragraph. README states this to users; widening any item makes that section wrong.

- `wsl.exe -l -q`, with a fixed argument list and resolved to its absolute path under the system
  directory (`windows.GetSystemDirectory`) rather than through `PATH`, is **the only process it may
  ever create**. Nothing is executed inside a distribution; home directories are listed over UNC.
- Any name enumerated from another operating system passes `isPlainName` before it becomes part of a
  path, so a distribution or directory named `..\..\something` cannot reach outside the tree.
- One network destination, and it is the vendor's own API.
- Two files written: the config document, and the icon files systray puts in `%TEMP%`. No registry
  write, no startup entry, no service, no scheduled task, no shortcut.
- No dynamic code loading of its own, and no elevation: it carries no manifest requesting one, so
  Windows runs it as invoker by default (§2.4).

### 3.2 Never write to the credential file

No OAuth flow, no refresh, no rewrite. Anthropic's `refresh_token` rotates on use, so refreshing
here would invalidate the copy the CLI holds and log the user out of their CLI. Expiry degrades
instead: `quota.Unauthorized` → `poll.DegradedInterval` → a message telling the user to run `claude`
once.

`credential.Secret` redacts through `String`, `GoString` and `MarshalJSON`, enforced by a test;
`Reveal()` in the outbound `Authorization` header is its only legitimate call site. The credential
document decodes only the three fields the app uses — naming `refreshToken` in that struct would
materialize a long-lived secret in this process for no purpose. Never put a secret on a command
line, never retain one past its use.

### 3.3 `FetchError.Kind` drives both the cadence and the message

It is the single input to the retry policy (`poll.backoff`) and to the user-facing text
(`tray.humanizeError`), so misclassifying one produces a wrong cadence *and* a wrong instruction.

| Kind | Poller reaction |
|---|---|
| `Unauthorized`, `Protocol` | `DegradedInterval` — waiting cannot fix these |
| `RateLimited` | `max(Retry-After, interval)`, with `Retry-After` itself bounded at 24 h |
| `Transient` | doubles per consecutive failure, capped at `MaxBackoff` — or at the configured interval when the user chose a slower one |

**Backoff may never poll sooner than the configured cadence.** A ceiling below the interval turns
backoff into the one thing it exists to prevent.

`credential.FailureKind` is a parallel taxonomy: `Absent`/`Unreadable` mean "try the next candidate"
(`credential.IsAbsent`), the rest mean "stop and tell the user".

### 3.4 The 300 s poll floor

Asserted in three places that behave differently: `poll.New` and `poll.Poller.SetInterval` silently
raise a faster interval (callers may go slower, never faster), while `config.Validate` rejects one
with a message. Change all or none. `tray.IntervalPresets` never offers a cadence below it, and a
test asserts every preset survives `config.Validate`.

### 3.5 A settings change is persisted before it is applied

The tray derives a whole document through `tray.WithInterval`/`WithLanguage`, which validate the
entire config rather than the changed field, then persists it through `Wiring.SaveSettings` — the
tray never learns the config path. Only after the write succeeds does the change reach the poller
and the presenter, so the menu can never show a state that is not on disk. A new cadence governs the
delay computed after the next poll; a timer already waiting is never cut short.

### 3.6 Persisted keys versus display text

`quota.MeterID` (`<vendor>:<kind>`) and `anthropic.VendorKey` are keys: stable across releases even
if the vendor renames its own field, and the identifier `internal/i18n` looks a label up under.
`Snapshot.Product` is the opposite — display text the provider owns ("Claude"), free to change,
never a key. Product branding belongs in the provider package, not in `internal/i18n`, because it is
vendor knowledge and is identical in every language.

### 3.7 Meter order is load-bearing

It sets menu row order and decides which meters survive tooltip truncation at 127 runes and the
fixed `maxMeterRows = 6` menu slots. Never sort it, never build it by iterating a map. It also
decides which row states a shared reset countdown: `Presenter.Rows` suppresses one that repeats the
row immediately above, so reordering moves the countdown to a different row.

### 3.8 The menu is allocated once

systray can add an item but never remove one, so every widget — meter rows, the two heading rows,
the `maxDetailRows` submenu rows, the settings items — is created in `onReady` and afterwards only
shown or hidden. A row with nothing to say is hidden, never left blank, and a submenu with no
visible rows hides its parent too. **A separator cannot be hidden at all**, which is why
`HeaderRow` falls back to `tray.AppName` instead of ever returning empty: the first group would
otherwise open with a divider above nothing. `maxDetailRows = 4` is the ceiling `DetailRows` can
produce (two account fields, since the third always heads the menu, plus two preference fields) and
a test holds it there, because the platform layer cannot allocate a fifth.

**Windows popup menus have no per-item tooltip.** systray's Windows backend drops the argument and
hover help would have to be owner-drawn, so a row that cannot say what it means in its own caption
cannot say it at all. The tooltip catalogue keys do not exist for that reason.

### 3.9 Menu row grammar, and where external text stops being data

Every row obeys one grammar: what it is on the left, what it currently reads on the right.
`tray.MenuRowTitle` puts the right column after `\t`, which Windows menus draw flush right; spaces
cannot align a column in a proportional font. systray sets items as `MFT_STRING` (no owner-draw),
which is what makes the shell's own tab handling apply — and also what rules out per-item font
weight, size, colour, icons and animation. A row with nothing to put on the right carries no tab,
because an empty right column widens every other row. Because the gauge is a fixed width and the
column is flush right, the figures beside it align with no padding.

`MenuRowTitle` is also the last hop before the shell for text this app only reads — an account name,
an organization, a vendor's own meter label. `&` is consumed as a mnemonic marker, a control
character opens a spurious column break, a `Cf` override reverses the row. Every field is
neutralized there, and an i18n test asserts no catalogue string needs it.

### 3.10 Numbers

**Money never passes through `float64`**: `quota.Money` is minor units and `Exponent` is
presentation only. **`Percent` is not clamped at 100** in the model — a vendor's overage figure is
preserved; only `trayicon.Render` and `tray.progressBar` clamp, for display, and they quantize by
the same rule so the icon and the menu can never disagree.

### 3.11 Concurrency and time

- **`tray.Run` must own the main goroutine** on Windows: systray locks the OS thread holding the
  message loop. The poller runs in a goroutine, and `updates` is a capacity-1 *signal* rather than a
  queue — the UI re-reads `Controller.State()`, so a coalesced signal cannot show stale data.
- **Nothing the UI calls may block on I/O it did not ask for.** `credential.Cache.Source()` is
  atomic precisely because discovery can hold the cache lock for as long as starting a stopped WSL
  distribution, and the tray reads it on every update.
- **Time is injected, never called:** `Poller.now`/`Poller.after`, `Cache.now`,
  `anthropic.Provider.now`, and explicit `now` parameters throughout `internal/tray`. Keep new code
  in this shape or its tests become clock-dependent.

---

## 4. The CLI's own documents

`identity.Cache` takes the credential path from `credential.Cache` rather than resolving a home
directory, which is what keeps the account shown in the menu tied to the subscription being polled.
It reads two documents with different policies:

- `.claude.json` (the account) at most once per credential path, since it changes only on
  re-authentication.
- `.claude/settings.json` (the configured model and effort) on every call, since the user can
  rewrite it at any moment.

Both are read independently, both reads are size-bounded, and every failure is non-fatal: one
document failing hides its own rows only, and polling continues either way.

**Neither the model nor the effort exists remotely**, and the local file gives a *default*, not what
a session is running: the CLI resolves that from a runtime `/model`, an environment variable and
project-level settings, none of which this app can observe. The labels say "default" for that
reason — `lastModelUsage` in the state document is worse still, being per project directory and
written at session end. `settings.json` also holds an `env` block that can carry other services'
credentials, so only the two fields are decoded and no error path quotes the document.

---

## 5. Adding a provider

Implement `quota.Provider` under `internal/provider/<name>/`. The core does not change.

```go
type Provider interface {
    Vendor() string
    Fetch(ctx context.Context) (*quota.Snapshot, error)
}
```

The contract:

- Every returned failure is a `*quota.FetchError` with the correct kind (§3.3). An unrecognizable
  document is `Protocol` — never a panic, never a zero value passing as valid.
- No field of the remote response is mandatory. These endpoints are undocumented and change without
  notice.
- `MeterID` uses the vendor prefix and stable values (§3.6). A meter's `Label()` is the vendor's own
  kind string, not display text: `internal/i18n` translates by `MeterID` and falls back to that raw
  kind, which is what lets a window a vendor adds tomorrow appear without a code change.
- `Snapshot.Product` is what the vendor sells; `Vendor()` is the key.
- Meter order within the snapshot is significant (§3.7).
- Credentials arrive through a local `CredentialSource` interface, so the package knows nothing
  about WSL, paths or caching.
- Implementations are safe for concurrent use and must not block past the context deadline.
- Ship a fixture reproducing the real response, including fields whose value is null, so schema
  drift is caught offline.

---

## 6. Conventions

- Every user-visible string resolves through `internal/i18n`. `en-US` is the default catalogue and
  `pt-BR` is opt-in through the config file; a string literal reaching the UI directly is a defect.
  Code, comments and identifiers are English.
- Comments carry invariants, security assumptions, platform constraints and non-obvious reasoning
  only. Match the existing density: no mechanical comments, no transitory notes, no commented-out
  code, and no reference to a document expected to disappear.
- Named constants documented with the measurement or platform limit that justifies the value
  (`maxTooltipRunes`, `borderWidth`, `maxCredentialBytes`). No bare literals.
- Errors are typed where the caller must branch on them (`quota.FetchError`, `credential.Failure`)
  and wrapped with `%w` otherwise. No sentinel string matching, no swallowing, no log-and-continue.
- Resources are released with `defer` at the point of acquisition. No leaked descriptors, goroutines
  or timers.
- Live tests are gated on `testing.Short()` (`live_e2e_test.go`, `smoke_test.go`).
- `go.mod` states the real dependency graph; run `go mod tidy` when imports change.

---

## 7. Security requirements

The credential invariants in §3.1 and §3.2 are the floor, not the whole rule.

- No secret, token, path-derived identifier or account detail reaches a log line, an error message,
  a temporary file, or a command line — not even truncated.
- Every external input surface states where it is validated and what it does on rejection. **Bound
  every read** from a file, a socket or a child process: an undocumented producer can always grow
  its output.
- Never resolve an executable through `PATH`, and never build a path from a name that has not been
  validated as a single component.
- **Never make a permanent change to the user's environment without explicit prior approval:**
  registry keys, startup entries, services, scheduled tasks, shortcuts, files outside this app's own
  config directory. When a feature needs one, stop, explain why, list the alternatives, and wait.
- Every file the app creates has a single documented purpose and is described in README. Nothing is
  written that no feature reads.
- Nothing in this codebase may resemble what an EDR classifies as malicious: no persistence, no
  injection, no hooking, no dynamic code loading, no elevation, no covert channel, no collection of
  anything the user did not ask to see.

---

## 8. Testing

Every behaviour ships with tests covering, where they apply: the happy path, boundaries (empty,
single, maximum, off-by-one), error handling, invalid input, regressions for defects already fixed,
and the cases that would corrupt state or leak data.

Tests are deterministic, order-independent and share no mutable state; clocks, filesystem and
network are injected or stubbed. Assert observable behaviour, never an implementation detail — a
test whose only purpose is to execute a line is not a test.

---

## 9. Review checklist

Applied to every change, and the standard the codebase was audited against:

1. **Correctness first.** Does the change hold under the invariants in §3? Integer overflow,
   clock skew, partial failure and empty input are all in scope.
2. **Dead weight.** No unused function, type, constant, field or file; no duplicated logic; no
   abstraction without two concrete call sites or a layer boundary to sit on.
3. **Resource discipline.** Every open closed, every goroutine joined or cancelled, every read
   bounded, every lock released — and no lock held across I/O a caller did not ask for.
4. **Attack surface.** New process, path, file, network destination or environment variable? It has
   to survive §3.1 and §7, or it does not land.
5. **Leakage.** Nothing sensitive in errors, logs, temp files or command lines.
6. **Layering.** Did a package just learn about a vendor, WSL, Windows or a file format outside its
   own responsibility? That is a design error, not a detail.
7. **Cost.** Simplicity, readability and maintainability outrank cleverness. New behaviour slots
   into the existing seams — `quota.Provider`, `CredentialSource`, injected clocks, the pure/glue
   split — rather than reshaping them.
8. **Proof.** `gofmt -l .`, `go vet ./...`, `GOOS=windows go vet ./...`, `go test -short -race ./...`
   and the Windows-tagged tests through binfmt, before calling anything done.
