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

### 1.1 What CI runs, and what it cannot

`.github/workflows/ci.yml` is §9.8 executed on every push and pull request against `master`. It
holds no build knowledge of its own: the compile step is the release command above, character for
character, and changing one without the other is the drift the duplication exists to make visible.

It differs from the local loop in four ways, each deliberate:

- A `windows-2022` job runs `go test -short ./...` natively, which is the only way the
  Windows-tagged tests run without WSL binfmt. It omits `-race`, since that would pull a C toolchain
  onto the runner to re-check code the Linux job already raced.
- The binary is compiled twice and the two results compared, because README promises a byte-identical
  executable per commit and a promise nothing checks is a wish. The second build is a separate job on
  a fresh runner with the build cache disabled: on one machine, back to back, `-trimpath` has already
  removed every variable the comparison could have caught. `GOTOOLCHAIN: local` is what keeps it
  meaningful either way — a silently downloaded compiler is a different binary. Both jobs run the
  release command character for character, `-o` included, so what they compare is a digest rather
  than two files with different names.
- `govulncheck` runs against the host target and against `GOOS=windows`, for the same reason `go vet`
  runs twice: the `_windows.go` files are invisible to a host-target analysis. It is pinned to a
  version, never `@latest`, so the gate cannot change meaning between two runs of one commit.
- The live test never runs: `-short` everywhere, because CI holds no credential and never will.

Only `publish` waits on all of it. The build itself is gated on nothing, because it consumes no
output of the validators and parking it behind the slowest runner only delayed the release; a pull
request that fails a lint still produces a binary to inspect. The consequence is that an artifact
existing does not imply the commit passed — `publish` is the only place that may draw that
conclusion, and it is the only place that does.

On `master` it then replaces the `edge` prerelease with the artifact it just validated — downloaded,
not rebuilt, so the published bytes are the bytes that passed. What proves they are is a digest the
build job published as a job *output*: the `.sha256` sidecar travels inside the artifact, so anything
able to substitute the binary can substitute the sidecar with it, and `sha256sum -c` alone would
prove only that the archive did not corrupt in transit. The sidecar is still checked, for the one
thing it can establish — that the file users download agrees with the hash they download beside it.

`build` also attests the binary's provenance (`actions/attest-build-provenance`, Sigstore-signed through
this run's own OIDC identity, no key this repository holds or could leak) and is the only job carrying
`id-token: write` and `attestations: write` — a job's `permissions:` block replaces the workflow-level
default rather than adding to it, so those two scopes exist nowhere else, including `publish`, which
only serves what `build` already signed. Every `uses:` in the file names a 40-hex commit SHA rather than
a tag, with the tag it resolved to at pinning time kept as a trailing comment: a tag is the one thing in
this pipeline its owner, not this repository, controls the meaning of, and repointing it would run
inside a job holding this repository's `GITHUB_TOKEN`.

The tag is `edge` rather than `master` because a tag sharing a branch's name makes every later
`git rev-parse master` ambiguous. Publishing is delete-then-create: `gh release edit` cannot move an
existing tag, so an amended release would go on naming the commit that first created it. That is also
why a push is never cancelled mid-run (`cancel-in-progress` is true for pull requests only) — a run
killed between the delete and the create leaves no release at all. The pair is retried as a unit for
the same reason, each attempt re-running the delete: the window between them is the one state README
says cannot exist, and re-running the delete is also what absorbs the `422` the API returns while a
just-deleted tag ref is still propagating.

---

## 2. Architecture

### 2.1 Layering

```
credential.Cache ─┬→ provider/anthropic → poll.Poller ─┬→ tray → i18n
                  └→ identity.Cache ───────────────────┘
                          ↘ internal/quota ↙

one tray.ProviderWiring per vendor: {Controller, CLIReader}
```

Dependencies flow one way and converge on `internal/quota`, which imports no project package —
changing it changes every layer above it. `cmd/meterai/main.go` is the only place that knows how the
pieces connect; nothing below it learns a file path, a vendor or a platform it does not own.

### 2.2 Packages

| Package | Owns | Must not know about |
|---|---|---|
| `internal/quota` | the vendor-neutral model, the fetch-error taxonomy, the local escalation thresholds, the `Provider` interface | anything in this project |
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

The tray derives a whole document through one `tray.With*` function per setting, which validate the
entire config rather than the changed field, then persists it through `Wiring.SaveSettings` — the
tray never learns the config path. Only after the write succeeds does the change reach the poller
and the presenter, so the menu can never show a state that is not on disk. A new cadence governs the
delay computed after the next poll; a timer already waiting is never cut short. A new threshold, by
contrast, reclassifies the reading already on screen: `apply` re-renders from the same snapshot.

Adding a setting is adding a `With*`, a row, and its widgets to `newMenuView`, `retitle` and
`syncSettingChecks`. Nothing else in the tray learns about it.

### 3.6 Persisted keys versus display text

`quota.MeterID` (`<vendor>:<kind>`) and `anthropic.VendorKey` are keys: stable across releases even
if the vendor renames its own field, and the identifier `internal/i18n` looks a label up under.
`Snapshot.Product` is the opposite — display text the provider owns ("Claude"), free to change,
never a key. Product branding belongs in the provider package, not in `internal/i18n`, because it is
vendor knowledge and is identical in every language.

### 3.7 Meter order is load-bearing

It sets menu row order and decides which meters survive the tooltip budget (§3.13) and the fixed
`maxMeterRows = 6` menu slots. Never sort it, never build it by iterating a map. It also decides
which row states a shared reset countdown: `Presenter.MeterRows` suppresses one that repeats the row
immediately above, so reordering moves the countdown to a different row.

### 3.8 The menu is allocated once, in four groups

systray can add an item but never remove one, so every widget — the heading, the provider and
preferences rows, the meter rows, the provider list and its per-provider account rows, the settings
items — is created in `onReady` and afterwards only shown or hidden. A row with nothing to say is
hidden, never left blank, and a submenu with no visible rows hides its parent too.

Four separators split the first level into what the app is, what it is currently reading, how fresh
that reading is, and where to go next:

```
meterAI                              v0.1.0     ← the app, fixed, never hidden
───────────────────────────────────────────────
Anthropic                        Claude Pro     ← the active provider
Opus • High Effort                              ← what its CLI is configured to use
Session (5h) · resets in 2h54   23%  ▄▄▁▁▁▁▁▁▁▁
Weekly (7d) · resets in 1d01h   74%  ▄▄▄▄▄▄▄▁▁▁
───────────────────────────────────────────────
Updated 1m ago · next in 3m
Refresh now
───────────────────────────────────────────────
Providers                                     ▸ ← one entry per configured provider
Settings                                      ▸
Quit
```

Every row of the second group starts at the same margin — nothing is indented under the provider,
because stepping one row in and back out breaks the column of names the group is read down.

**A separator cannot be hidden at all**, and that constraint decides two things. `HeaderRow` names
the app rather than the provider, so it is never empty and the first group can never open with a
divider above nothing. And **only one provider can hold the first level**: stacking a second
provider's block there would need a separator between the two, which a provider with nothing to
report could not hide. `Subscriptions.Active` is that position — the first configured provider —
and every other provider is reached through the list, which grows without the first level changing
shape.

A provider is named twice and qualified once: `ProviderListRow` puts the vendor alone in the list, so
the list reads as a column of names, and `ProviderRow` heads that provider's own submenu with the
vendor *and* what it sells. Stating the plan on both the row that opens a submenu and the row behind
it is the same fact twice, one click apart.

`maxAccountRows = 3` is the ceiling `AccountRows` can produce, one per field of `identity.Account`,
and a test holds it there because the platform layer cannot allocate a fourth.

The group under the first separator assumes `Wiring.Providers` is non-empty and that every provider
answers `Vendor()` with something — both hold by construction, and the cost of neither holding is two
adjacent separators, not a crash.

**Windows popup menus have no per-item tooltip.** systray's Windows backend drops the argument and
hover help would have to be owner-drawn, so a row that cannot say what it means in its own caption
cannot say it at all. The tooltip catalogue keys do not exist for that reason.

The split between the two levels is by how often a value is read, not by which document supplied it:
quota figures and the configured model are operational state and stay on the first level; the fields
that identify an account are consulted rarely and are read by everyone watching a shared screen, so
they live behind the provider's own entry. A test asserts no account field reaches the first level.

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

Length is settled at that same hop, by `maxMenuFieldRunes`. A display name and an organization are
the vendor's own profile response by way of the CLI's state document, and a utilization percentage is
a JSON number with no stated range — `1e300` renders as three hundred digits. Nothing this app writes
comes near the bound; a caption wider than the screen is a popup menu nobody can use. The bound
applies *after* the substitutions above, because doubling every `&` is itself a way to grow a field.

### 3.10 The two usage-alert thresholds are an ordered pair, and the menu keeps them so

`quota.Thresholds` owns the whole rule — the defaults, the bounds, the
percent→`Severity` mapping and its validation — because that mapping is what
`Severity` means locally. `config.Config` composes one under `usageAlerts` and
adds nothing to it; anything that classifies a reading goes through
`cfg.UsageAlerts.SeverityFor`, and nothing anywhere carries a threshold of its
own. `Config.UnmarshalJSON` also reads the flat `warnAtPercent`/`criticalAtPercent`
of earlier releases, so an upgrade never silently reverts a tuned pair.

`CriticalAtPercent` must be **strictly above** `WarnAtPercent`: `SeverityFor`
tests critical first, so an equal pair is a warning level that exists in the
settings and can never be displayed.

`tray.WarnPresets`/`CriticalPresets` are one list with the one end each cannot
hold removed, and `tray.WithWarnThreshold`/`WithCriticalThreshold` take the
chosen value and step the *other* threshold to the adjacent preset when the pair
would otherwise invert. That is what keeps a rejection unreachable through the
UI: a Windows popup menu has no per-item hover text (§3.8), so a greyed item
cannot say why it is greyed, and an error line in place of a choice the menu
itself offered is worse than moving a companion the user can see move. Both
parent rows carry their value, and `syncSettingChecks` resyncs *both* lists after
either change, because one of them moved without being clicked.

### 3.11 Numbers

**Money never passes through `float64`**: `quota.Money` is minor units and `Exponent` is
presentation only. **`Percent` is not clamped at 100** in the model — a vendor's overage figure is
preserved; only `trayicon.Render` and `tray.progressBar` clamp, for display, and they quantize by
the same rule so the icon and the menu can never disagree.

### 3.12 Concurrency and time

- **`tray.Run` must own the main goroutine** on Windows: systray locks the OS thread holding the
  message loop. The poller runs in a goroutine, and `updates` is a capacity-1 *signal* rather than a
  queue — the UI re-reads `Controller.State()`, so a coalesced signal cannot show stale data.
- **Nothing the UI calls may block on I/O it did not ask for.** `credential.Cache.Source()` is
  atomic precisely because discovery can hold the cache lock for as long as starting a stopped WSL
  distribution, and the tray reads it on every update. `identity.Cache` is the same shape for the
  same reason: its accessors answer from an `atomic.Pointer`, the read runs on a goroutine of its
  own, and a caller that asks before one has landed is told the answer is not known yet. The
  goroutine is injected (`Cache.run`) so the caching rules are asserted inline, the way the clocks
  below are.
- **An announcement may only follow a real change.** `identity.Cache` redraws the UI when its
  documents change, and that redraw is what asks for them again. Announcing every read — which
  comparing errors by identity would do, since each failed read mints a new value — would make the
  UI schedule the read that notified it, forever.
- **Time is injected, never called:** `Poller.now`/`Poller.after`, `Cache.now`,
  `anthropic.Provider.now`, and explicit `now` parameters throughout `internal/tray`. Keep new code
  in this shape or its tests become clock-dependent.

### 3.13 The tooltip has 63 characters, and no line may lose its figure

`NOTIFYICONDATA.szTip` is declared as 128 WCHARs, but the shell only honours that for an icon that
announced itself through `NIM_SETVERSION`. systray never issues it, so the shell reads the legacy
64-WCHAR field: **63 characters plus the terminator**, and everything past that is discarded
silently, mid-line. That is why `maxTooltipRunes = 63` and why the tooltip is a different
composition from the menu rather than a copy of it — the reset countdowns and the gauges are dropped
so that two windows and a status line fit.

`joinWithinBudget` includes a meter line whole or not at all. A meter line cut mid-way loses the
figure at the end of it and reads exactly like a meter that reported none, which is the one
misreading this text must not produce. The trailing status line is prose and is elided instead,
because a sentence still reads when cut short.

---

## 4. The CLI's own documents

`identity.Cache` takes the credential path from `credential.Cache` rather than resolving a home
directory, which is what keeps the account shown in the menu tied to the subscription being polled.
It reads two documents, and **when it re-reads them is decided by what the read costs**, which is the
second thing it takes from `credential.Cache`: `SourceIsRemote()` reports whether the credential file
is served by another operating system. `identity` never learns what that system is — it reads the
price off that one call, and the knowledge of what a `\\wsl.localhost` path is stays in
`internal/credential`, which owns it.

- `.claude.json` (the account) at most once per credential path, whatever the price, since it
  changes only on re-authentication and the CLI caches remote feature flags in the same file.
- `.claude/settings.json` (the configured model and effort) on every call **from a local source**,
  where the price is an open and a model changed in the CLI should reach the menu on the next poll.
- From a **remote** source, neither is re-read on a cadence. Opening a path under a WSL distribution
  starts that distribution if it is stopped, and a five-minute cadence would keep another operating
  system awake to redraw a decorative row. `credential.Cache` already refuses to re-read the
  credential itself for exactly this reason; a caption must not undo that.

`Cache.Invalidate()` is what re-reads a remote source, and it is reached from the Refresh command and
from nowhere else: the click turns a possible virtual-machine boot from a cost the app imposes into
one the user asked for. It re-reads **both** documents, the account included — signing out and back
in as somebody else rewrites that document in place, leaving the path unchanged, so the per-path rule
above would otherwise never notice. The rule lives in `tray.refreshProviders` rather than beside the
widgets, so the one behaviour behind that click is testable on any host, and it invalidates only the
providers that accepted the poll: re-reading for a poll that will not happen pays the boot for
nothing.

Both are read independently, both reads are size-bounded, and every failure is non-fatal: one
document failing hides its own rows only, and polling continues either way.

**Neither the model nor the effort exists remotely**, and the local file gives a *default*, not what
a session is running: the CLI resolves that from a runtime `/model`, an environment variable and
project-level settings, none of which this app can observe. `lastModelUsage` in the state document is
worse still, being per project directory and written at session end. `settings.json` also holds an
`env` block that can carry other services' credentials, so only the two fields are decoded and no
error path quotes the document.

Both values reach the menu title-cased, and only their first rune is touched: the rest is what the
user typed and may carry capitals that matter. The row is a caption rather than a quotation of the
file, and `opus` beside a column of capitalized window names reads as a defect. The effort does not
travel alone either — `High` names no setting by itself, so it is interpolated into the `EffortLevel`
catalogue entry, and the two are joined into one row rather than given a label column each.

---

## 5. Adding a provider

Implement `quota.Provider` under `internal/provider/<name>/`. The core does not change, and neither
does the menu: `main.go` appends a `tray.ProviderWiring` — that vendor's poller and its `CLIReader` —
to `Wiring.Providers`, and the provider list allocates one entry per element (§3.8). Nothing in
`internal/tray` counts providers or assumes there is one.

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
