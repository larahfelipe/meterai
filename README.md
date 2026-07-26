# meterAI

A Windows notification-area monitor for AI subscription usage quotas. It shows
how much of each allowance has been consumed, how long until each window
resets, and warns as a limit approaches — without asking you to log in again.

---

## Core principle: read, never write

There is no public API for querying a consumer subscription's quota — the
Claude Pro/Max five-hour window, for instance. Those figures exist only behind
internal endpoints that the official CLIs use, authenticated with OAuth tokens
those CLIs have already written to disk.

meterAI reads those tokens and **never modifies them**:

- no OAuth flow of its own;
- no token refresh;
- no rewriting of the credential file.

The reason is concrete. Anthropic's `refresh_token` rotates when used. If this
app refreshed the token on its own and did not write the result back, the copy
the CLI still holds would become invalid and the user would be logged out of
their CLI. Writing it back, in turn, would race the CLI's own file lock. The
choice is to remain strictly a reader: when the token expires, the app enters a
degraded state and says that running the CLI once will renew it.

Practical consequence: **the app depends on the official CLI having been used at
least once**, and stops updating if the token expires without the CLI being used
again.

---

## Architecture

```
              ┌──────────────────────┐
              │  .credentials.json   │  (written by the official CLI;
              │  native Windows or   │   this app only reads it)
              │  \\wsl.localhost\... │
              └──────────┬───────────┘
                         │ read on demand
              ┌──────────▼───────────┐
              │  credential.Cache    │  re-reads only near expiry
              └──────────┬───────────┘
                         │ Token(ctx)
              ┌──────────▼───────────┐
              │  provider/anthropic  │  HTTPS → normalization
              └──────────┬───────────┘
                         │ quota.Snapshot
              ┌──────────▼───────────┐
              │  poll.Poller         │  cadence derived from failure kind
              └──────────┬───────────┘
                         │ signal + State()
              ┌──────────▼───────────┐
              │  tray                │  icon, tooltip, menu
              └──────────────────────┘
```

The dependency graph is acyclic and converges on `internal/quota`, which imports
no other package in the project.

### Vendor-neutral data model

Vendors expose quota in structurally different shapes: rolling percentage
windows, daily counters, a money balance. `quota.Meter` is a sealed union with
exactly two variants, which makes handling exhaustive by construction:

| Variant | Represents | Fields |
|---|---|---|
| `quota.Utilization` | percentage of an allowance | `Percent`, `Reset`, `Level`, `IsActive` |
| `quota.Balance` | monetary balance | `Used`, `Limit`, `Percent`, `Level` |

Rules that hold for every provider:

- `MeterID` has the form `<vendor>:<kind>` (e.g. `anthropic:session`) and is a
  stable key — it does not change across releases, even if the vendor renames
  its field.
- Money never passes through `float64`. `quota.Money` stores minor units
  (`AmountMinor`, `Currency`, `Exponent`); the exponent controls presentation
  only.
- `Percent` is not clamped at 100: vendors that permit overage report values
  above it, and the model preserves the real figure.

### Error taxonomy

Every fetch failure is a `*quota.FetchError` carrying one of the four kinds
below. The kind determines what the poller does and what the UI tells the user:

| Kind | Origin | Reaction |
|---|---|---|
| `Unauthorized` | 401/403, expired token | waits for user action |
| `RateLimited` | 429 | honours `Retry-After` |
| `Transient` | network failure, 5xx, timeout | exponential backoff |
| `Protocol` | uninterpretable response | waits for a fix in the app |

The distinction that matters: `Transient` and `RateLimited` resolve by waiting;
`Unauthorized` and `Protocol` do not, and retrying them only reproduces the same
error in a loop.

---

## Requirements

- Windows 10 or later (runtime target).
- Go 1.25 or later (to build only).
- An authenticated official CLI — `claude` for the Anthropic provider. It may be
  installed on native Windows or inside a WSL2 distribution.

External dependencies of the Windows binary: `fyne.io/systray` and
`golang.org/x/sys`. Neither requires CGO.

---

## Building

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/meterAI.exe ./cmd/meterai
```

`CGO_ENABLED=0` is a project requirement, not a convenience: it allows cross
compilation from Linux or WSL with no C toolchain. `-H=windowsgui` suppresses
the console window that would otherwise sit behind the tray icon.

On non-Windows systems the same `main` compiles and runs headless, printing each
update to stderr. This exists so the full pipeline — credential discovery,
polling, backoff, formatting — can be exercised on the development host.

---

## Usage

Run `dist\meterAI.exe`. The icon appears in the notification area.

- **Hovering** shows each quota window, its percentage, and the time to reset.
  The tooltip carries no gauges: the shell caps it at 127 characters, and ten
  cells per meter would push the status line out of it.
- **Clicking** opens a menu headed by the account and plan being monitored, then
  a gauge per meter, a *Details* submenu carrying the account's e-mail and
  organization, a *Settings* submenu, and finally *Refresh now* and *Quit*. A
  meter with no stated allowance, such as an uncapped balance, shows no gauge
  rather than an empty one.
- **Settings** changes the update cadence and the interface language without a
  restart. Each change is written to the config file first and only then applied,
  so the menu never shows a setting that is not on disk; if the write fails, the
  status line says so and nothing changes. A new cadence takes effect after the
  next poll — a wait already in progress is never shortened, since that would be
  a way around the polling floor.
- The **icon colour** follows severity: green, amber, red.
- The icon turns **grey** when the displayed figures are no longer being
  confirmed; the menu states how old they are.

### Starting with Windows

meterAI does not install itself anywhere. It writes no registry key, no startup
entry, no service and no scheduled task, and it never will without being asked:
the only thing it creates is its own config file under `%APPDATA%\meterAI`.

To have it start with the session, use the mechanism Windows already provides —
press `Win+R`, run `shell:startup`, and drop a shortcut to `meterAI.exe` in the
folder that opens. Removing the shortcut undoes it, with nothing left behind.

Only one instance runs per Windows logon session, enforced by a named kernel
mutex. A second copy exits quietly with exit code `3`. The scope is per session,
not machine-wide: two users on the same machine each get their own instance,
since each has their own subscription and credential.

---

## Configuration

A JSON file, created on first run with owner-only permissions:

```
%APPDATA%\meterAI\config.json
```

```json
{
  "credentialPath": "",
  "pollInterval": "5m0s",
  "warnAtPercent": 75,
  "criticalAtPercent": 90,
  "language": "en-US"
}
```

| Field | Meaning |
|---|---|
| `credentialPath` | Explicit path to the credential file. Empty enables autodiscovery. |
| `pollInterval` | Polling cadence. Values below the safe minimum are rejected with a message, not silently corrected. |
| `warnAtPercent` | Local warning threshold. |
| `criticalAtPercent` | Local critical threshold. Must be greater than or equal to `warnAtPercent`. |
| `language` | Interface language: `en-US` (default) or `pt-BR`. Empty selects the default; an unsupported tag is rejected with the list of accepted ones. |

`pollInterval` and `language` are also editable from the *Settings* submenu,
which writes this same file. The menu offers only cadences at or above the floor,
so a choice made there can always be saved.

A populated `credentialPath` is **authoritative**: if that path fails, the app
reports an error rather than looking elsewhere. Falling through to another
candidate would mean silently monitoring a different account than the pinned one.

Local thresholds are combined with the vendor's own reported severity, with the
more severe of the two winning. The vendor is never overruled downward: if
Anthropic says critical, the icon says critical. These thresholds exist because
vendors classify as normal percentages well past the point a user wants to be
warned.

An existing but unreadable config file is an error and is never overwritten.
Missing fields are filled from the defaults, so documents written by earlier
releases remain valid. Writes are atomic (temporary file followed by a rename
within the same directory).

---

## Credential discovery

The credential file is not in a fixed place: it depends on how the CLI was
installed and where the login happened. The search runs in this order:

1. `credentialPath`, if configured — and only it.
2. `%USERPROFILE%\.claude\.credentials.json` (native Windows installation).
3. For every WSL distribution returned by `wsl.exe -l -q`, excluding system
   distributions: `\\wsl.localhost\<distro>\<home>\.claude\.credentials.json`.

`$HOME` inside the distribution is queried at runtime, never assumed: the WSL
account name is chosen at setup and need not match the Windows one.
Distributions belonging to other products (Docker Desktop, Rancher Desktop) are
skipped — probing them would boot a VM for nothing.

### Why UNC rather than `wsl.exe`

Access to the distribution's filesystem uses the `\\wsl.localhost` network path,
with `wsl.exe` only as a fallback when that fails. Reading over UNC is an order
of magnitude faster than spawning a subprocess, requires no UTF-16 output
decoding, and does not depend on `wsl.exe`'s flag format, which has varied
between versions.

### Stopped distribution

Both routes wake a stopped distribution, so the difference is not the route but
the access frequency. The access token lives for hours and the file reads in
milliseconds, so `credential.Cache` re-reads only when the cached token
approaches expiry — not on every poll. While the cached token remains valid, a
temporary WSL outage does not interrupt monitoring.

---

## Account details

The name, e-mail and organization shown in the menu are read from the CLI's own
state document, `.claude.json`, which sits **beside** the `.claude` directory
rather than inside it. The CLI fetches its profile once and caches it there, so
reading that cache costs no request, works offline, and spares this app a second
undocumented endpoint to depend on.

The path is derived from the credential file actually in use, never from the
running user's home directory. That is what guarantees the account on screen and
the quota being polled belong to the same installation — otherwise a credential
found inside WSL, or pinned through `credentialPath`, would be reported under
whichever account happened to be signed in on the Windows side.

This document is read-only to meterAI, exactly like the credential file. It is
also the CLI's private bookkeeping: it carries a schema version the CLI has
already migrated repeatedly, so every field is optional, the read is size-bounded,
and any failure — absent, unparseable, or simply not signed in — hides the
account rows instead of affecting polling. Account values never appear in an
error message or on a log line; a decoding failure reports its structural cause
only, because the document also holds the user's project paths.

---

## Cadence and recovery

| Situation | Next poll |
|---|---|
| Success | configured interval |
| Transient failure | doubles per consecutive failure, up to a cap |
| `429` | the server's `Retry-After`, never below the configured interval |
| Expired credential or changed schema | long degraded interval |

A successful poll resets the escalation. *Refresh now* has a minimum spacing
between invocations, charged at the moment the request is accepted — so a burst
of clicks cannot queue several polls in the window before the first response
arrives.

The undocumented endpoints publish no `RateLimit-*` headers, meaning there is no
server-side signal to calibrate cadence against. The default interval is
deliberately conservative, and shortening it is a gamble with account-level
downside.

---

## Project layout

```
cmd/meterai/            binary: wires the pieces and yields the main goroutine to the tray
internal/quota/         vendor-neutral model and error taxonomy
internal/credential/    location, parsing and caching of the credential file
internal/provider/      quota.Provider implementations, one per vendor
internal/poll/          scheduling and backoff
internal/config/        user settings
internal/i18n/          every user-visible string, one catalogue per language
internal/identity/      account details read from the CLI's cached profile
internal/tray/          pure formatting (format.go) + platform glue
internal/trayicon/      icon rendering in ICO format
```

Platform-specific code is isolated behind build tags in `_windows.go` and
`_other.go`/`_unix.go` files. All logic that produces visible text or pixels is
pure and lives in untagged files, which makes it testable on any host rather
than only on Windows.

> One toolchain detail worth remembering: Go **silently ignores** any directory
> named `vendor`. A package placed there falls out of build, `vet` and tests
> with no warning. That is why vendor implementations live under
> `internal/provider/`.

---

## Adding a provider

Implement `quota.Provider` in `internal/provider/<name>/`:

```go
type Provider interface {
    Vendor() string
    Fetch(ctx context.Context) (*quota.Snapshot, error)
}
```

The contract an implementation must honour:

- Every returned failure is a `*quota.FetchError` with the correct kind — that
  is what drives retry behaviour and the user-facing message.
- `MeterID` uses the vendor prefix and stable values. It is also the key a
  translation is looked up under, so renaming one silently drops the meter back
  to its untranslated name.
- A meter's `Label()` is the vendor's own kind string, not display text.
  `internal/i18n` translates by `MeterID` and falls back to that raw kind, which
  is what lets a window a vendor adds tomorrow appear without a code change.
- No field of the remote response is mandatory. Undocumented endpoints change
  shape without notice, so an unrecognizable document becomes `Protocol`, never
  a panic or a zero value passing as valid.
- Meter order within the `Snapshot` is significant: it sets row order in the UI
  and determines which meters survive tooltip truncation.
- Credentials arrive through the `CredentialSource` interface, so a provider
  needs to know nothing about WSL, paths, or caching.

The core does not change to accommodate a new vendor.

---

## Tests

```sh
go test -race ./...              # includes tests that hit the real API
go test -short -race ./...       # offline and deterministic only
```

Tests marked *live* query the real endpoint and are skipped under `-short`. They
are useful for detecting schema drift, but consume one request per run.

The Anthropic provider's parser is checked against a fixture reproducing the
real response shape, including internal codename fields with null values. That
is what allows a schema change to be detected without network traffic.

---

## Security

The only secret handled is the OAuth token read from disk. The protections are
structural rather than conventional:

- The `credential.Secret` type overrides `String()`, `GoString()` and
  `MarshalJSON()` to emit a redaction marker. An accidental
  `log.Printf("%v", creds)` or `json.Marshal` is incapable of emitting the
  token. A test fails the build if that property breaks.
- The only place in the code that reveals the token is the outbound
  `Authorization` header.
- Go's default redirect policy strips `Authorization` on a cross-host redirect,
  which prevents the token from being re-sent to another domain.
- The refresh token is never carried into the in-memory credentials at all.
  Since the app never refreshes, retaining a long-lived secret for the whole
  session would be exposure bought for nothing.
- No write path exists over the credential file.
- Reads are size-bounded, both for the local file and for the HTTP response, so
  a corrupt file or a hostile response cannot exhaust memory.

The config file is created with owner-only permissions: it holds no secret, but
it does hold a path into the credential store.

---

## Inherent limitations

- **The consumer-subscription quota endpoints are undocumented and
  unsupported.** They may change shape or disappear without notice. The app
  treats this as an expected condition — it reports and degrades — but there is
  no way to guarantee continued operation against an interface that is not a
  contract.
- **Icon precision.** The gauge is 24 rows tall, roughly 4 percentage points per
  row. Exact figures live in the tooltip and the menu.
- **Dependence on the official CLI.** Without a prior login, and without
  periodic use keeping the token renewed, there is nothing to read.

## Reference

`github.com/akitaonrails/ai-usagebar` — a Rust implementation for Linux, used as
the initial source for the endpoints and response shape.
