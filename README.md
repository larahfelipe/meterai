# meterAI

A Windows notification-area monitor for AI subscription usage quotas.

It sits in the tray and shows how much of each allowance has been consumed, how
long until each window resets, and warns as a limit approaches — reading the
credentials the official CLI already wrote, so there is nothing to log into.

```
 Anthropic                                          Claude Max
     Felipe
 ─────────────────────────────────────────────────────────────
 Session (5h) · resets in 2h13              47%  ▄▄▄▄▄▁▁▁▁▁
 Weekly (7d) · resets in 4d06h             100%  ▄▄▄▄▄▄▄▄▄▄
 ─────────────────────────────────────────────────────────────
 Updated 1m ago · next in 3m
 Refresh now
 Details                                                    ▸
 Settings                                                   ▸
 ─────────────────────────────────────────────────────────────
 Quit
```

## Features

- **Every quota window at a glance** — percentage, gauge and time to reset, with
  the tray icon coloured by severity and greyed when the figures are no longer
  being confirmed.
- **No login.** It reads the token the official CLI stored and never modifies it.
- **Finds the credential itself**, whether the CLI was installed on native
  Windows or inside a WSL2 distribution.
- **Settings in the menu** — polling cadence and interface language (`en-US`,
  `pt-BR`), applied without a restart.
- **Small footprint by design**: one process created, one network destination,
  two files written, no registry key, no startup entry, no elevation.

## How it works

There is no public API for a consumer subscription's quota — the Claude Pro/Max
five-hour window, for instance. Those figures live behind the internal endpoint
the official CLI uses, authenticated with an OAuth token that CLI has already
written to disk.

meterAI reads that token and **never writes it back**: no OAuth flow of its own,
no refresh, no rewrite. Anthropic's refresh token rotates when used, so
refreshing it here would invalidate the copy the CLI holds and log you out of
your own CLI. When the token expires, the app degrades and says that running
`claude` once will renew it.

Two practical consequences: the official CLI must have been used at least once,
and it has to keep being used often enough for its token to stay valid.

## Requirements

- Windows 10 or later, to run.
- An authenticated official CLI — `claude`, on native Windows or inside WSL2.
- Go 1.25 or later, to build.

The binary depends on `fyne.io/systray` and `golang.org/x/sys`. Neither needs
CGO.

## Install

There is no release build yet; produce one with:

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/meterAI.exe ./cmd/meterai
```

The build cross-compiles from Linux, WSL or macOS with no C toolchain, and is
reproducible: the same commit and toolchain produce a byte-identical executable,
so a published hash means something.

## Usage

Run `meterAI.exe`. The icon appears in the notification area.

- **Hover** for the quota windows and the status of polling.
- **Click** for the menu above: each meter on its own row, what the app is doing
  right now, _Refresh now_, _Details_ (account, organization, configured model),
  and _Settings_.
- **Settings** show the value in force beside their name and change the update
  cadence and the language without a restart. Each change is written to the
  config file before it is applied, so the menu never shows a setting that is not
  on disk.

Only one instance runs per Windows logon session, enforced by a named kernel
mutex; a second copy exits quietly with code `3`. Two users on the same machine
each get their own instance.

### Starting with Windows

meterAI does not install itself anywhere and writes no startup entry. Use the
mechanism Windows already provides: press `Win+R`, run `shell:startup`, and drop
a shortcut to `meterAI.exe` in the folder that opens. Removing the shortcut
undoes it, with nothing left behind.

## Configuration

`%APPDATA%\meterAI\config.json`, created on first run with owner-only
permissions and replaced atomically on every write:

```json
{
  "credentialPath": "",
  "pollInterval": "5m0s",
  "warnAtPercent": 75,
  "criticalAtPercent": 90,
  "language": "en-US"
}
```

| Field               | Meaning                                                                                                     |
| ------------------- | ----------------------------------------------------------------------------------------------------------- |
| `credentialPath`    | Explicit path to the CLI credential file. Empty enables autodiscovery.                                      |
| `pollInterval`      | Polling cadence. Below the safe minimum of 5 minutes it is rejected with a message, not silently corrected. |
| `warnAtPercent`     | Local warning threshold.                                                                                    |
| `criticalAtPercent` | Local critical threshold, at or above `warnAtPercent`.                                                      |
| `language`          | `en-US` (default) or `pt-BR`.                                                                               |

`pollInterval` and `language` are also editable from the _Settings_ submenu,
which writes this same file.

A populated `credentialPath` is **authoritative**: if that path fails, the app
reports an error instead of looking elsewhere, since falling through would mean
silently monitoring a different account than the pinned one.

Local thresholds are combined with the vendor's own severity and the more severe
of the two wins; the vendor is never overruled downward. A config file that
exists but cannot be parsed is an error and is never overwritten, and missing
fields are filled from the defaults, so documents written by earlier releases
keep working.

## What it touches

The whole footprint, and it is a ceiling rather than a default:

- **One process created**: `wsl.exe -l -q`, to list WSL distributions — started
  by its absolute path under `System32`, never through `PATH`, with a fixed
  argument list. Nothing is ever executed inside a distribution; its home
  directories are read over `\\wsl.localhost`.
- **One network destination**: the vendor's own API.
- **Two files written**: `%APPDATA%\meterAI\config.json`, and the tray icon that
  systray hands to Windows as a path in `%TEMP%` (a 4 KiB ICO named after its own
  content hash, at most 97 of them).
- **Nothing else**: no registry key, no startup entry, no service, no scheduled
  task, no dynamic code loading, and no elevation — it carries no manifest
  requesting one, so Windows runs it as invoker by default.

The only secret handled is the OAuth token, and the protections are structural:
it is redacted by its own type in every rendered form, its single reveal site is
the outbound `Authorization` header, the refresh token is never carried into
memory at all, and no code path writes to the credential file. Every read from a
file, a socket or a child process is size-bounded, and text coming from documents
the app only reads is neutralized before it reaches a menu caption.

## Development

```sh
go test -short -race ./...     # offline, deterministic — the default loop
go test -race ./...            # also hits the live endpoint (1 request per run)
go vet ./... && GOOS=windows go vet ./...
gofmt -l .
go run ./cmd/meterai           # headless: prints each poll to stderr
```

On non-Windows hosts the same `main` compiles and runs headless, printing each
update to stderr, so the whole pipeline — credential discovery, polling, backoff,
formatting — can be exercised where the credentials actually live. Because the
Windows-only files are excluded from a local build, `GOOS=windows go vet ./...`
is part of the loop rather than an afterthought.

Contributions should follow [CLAUDE.md](CLAUDE.md), which carries the
architecture, the invariants that span packages, and the review checklist.

## Project layout

```
cmd/meterai/            the binary: wires everything and yields the main goroutine to the tray
internal/quota/         vendor-neutral model, error taxonomy, Provider interface
internal/credential/    locating, parsing and caching the CLI credential file
internal/provider/      quota.Provider implementations, one directory per vendor
internal/poll/          scheduling, backoff and published state
internal/identity/      account and configured model, read from the CLI's own documents
internal/config/        user settings
internal/i18n/          every user-visible string, one catalogue per language
internal/tray/          presentation (pure) plus the Windows menu (platform glue)
internal/trayicon/      ICO rendering for the notification area
internal/buildinfo/     product name and version, shared by the binary and its outbound traffic
```

Platform-specific code is isolated behind build tags in `_windows.go` and
`_other.go`/`_unix.go` files; everything that produces visible text or pixels is
pure and lives in untagged files, testable on any host.

## Adding a provider

Implement `quota.Provider` in `internal/provider/<name>/` — `Vendor() string` and
`Fetch(ctx) (*quota.Snapshot, error)`. The core does not change to accommodate a
new vendor. [CLAUDE.md §5](CLAUDE.md) states the full contract; the two things
easiest to get wrong are classifying failures correctly, since that drives both
retry cadence and the message shown, and treating every remote field as optional.

## Limitations

- **The quota endpoints are undocumented and unsupported.** They may change shape
  or disappear without notice. The app treats that as an expected condition and
  degrades with an explanation, but no interface that is not a contract can be
  guaranteed.
- **Icon precision.** The gauge is 24 pixel rows tall, roughly four percentage
  points each; exact figures are in the menu and the tooltip.
- **Dependence on the official CLI.** Without a prior login, and without use
  keeping the token renewed, there is nothing to read.

## Acknowledgements

`github.com/akitaonrails/ai-usagebar` — a Rust implementation for Linux, used as
the initial source for the endpoint and response shape.

## License

MIT. See [LICENSE](LICENSE).
