# meterAI

A Windows notification-area monitor for AI subscription usage quotas.

It sits in the tray and shows how much of each allowance has been consumed, how
long until each window resets, and warns as a limit approaches — reading the
credentials each vendor's official CLI already wrote, so there is nothing to log into.

```
 meterAI                                                v0.1.0
 ─────────────────────────────────────────────────────────────
 Anthropic                                          Claude Max
 Opus • High Effort
 Session (5h) · resets in 2h13              47%  ▄▄▄▄▄▁▁▁▁▁
 Weekly (7d) · resets in 4d06h             100%  ▄▄▄▄▄▄▄▄▄▄
 ─────────────────────────────────────────────────────────────
 Updated 1m ago · next in 3m
 Refresh now
 ─────────────────────────────────────────────────────────────
 Providers                                                  ▸
 Settings                                                   ▸
 Quit
```

## Features

- **Every quota window at a glance** — percentage, gauge and time to reset, with
  the tray icon coloured by severity and greyed when the figures are no longer
  being confirmed.
- **No login.** It reads the token each vendor's official CLI stored and never
  modifies it.
- **Finds the credential itself**, whether the CLI was installed on native
  Windows or inside a WSL2 distribution.
- **Settings in the menu** — the usage-alert thresholds, the polling cadence and
  the interface language (`en-US`, `pt-BR`), applied without a restart.
- **Small footprint by design**: one process created, one network destination
  per configured provider, two files written, no registry key, no startup
  entry, no elevation.

## How it works

There is no public API for a consumer subscription's quota — the Claude Pro/Max
five-hour window, or the Codex 5-hour and weekly limits, for instance. Those
figures live behind each vendor's own internal endpoint, authenticated with an
OAuth token that vendor's official CLI has already written to disk.

meterAI reads that token and **never writes it back**: no OAuth flow of its own,
no refresh, no rewrite, for either provider. Anthropic's refresh token rotates
when used, and OpenAI's carries the same shape of risk, so refreshing either
here would invalidate the copy its CLI holds and log you out of it. When a
token expires, the app degrades and says that running that vendor's own CLI
command once (`claude`, `codex login`) will renew it.

Two practical consequences: each provider's official CLI must have been used at
least once, and it has to keep being used often enough for its token to stay
valid.

## Requirements

- Windows 10 or later, to run.
- At least one authenticated official CLI — `claude` and/or `codex`, on native
  Windows or inside WSL2.
- Go 1.25 or later, to build.

The binary depends on `fyne.io/systray` and `golang.org/x/sys`. Neither needs
CGO.

## Install

There is no versioned release yet. Every push to `master` publishes the binary
it built as the rolling [`edge`](https://github.com/larahfelipe/meterai/releases/tag/edge)
prerelease, together with the SHA-256 recorded at build time — check a download
with `sha256sum -c meterAI.exe.sha256`. That release is replaced in place on
every push, so it names no version and only ever describes the current tip. The
executable is unsigned: SmartScreen warns the first time it runs.

The checksum proves the download matches what CI recorded; it says nothing about
who built it. For that, verify the binary was produced by this repository's own
workflow, not merely by someone holding a hash that matches:

```sh
gh attestation verify meterAI.exe --repo larahfelipe/meterai
```

To produce the same binary yourself:

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w -H=windowsgui" -o dist/meterAI.exe ./cmd/meterai
```

The build cross-compiles from Linux, WSL or macOS with no C toolchain, and is
reproducible: the same commit and toolchain produce a byte-identical executable,
so a published hash means something. CI compiles each commit on two independent
runners and refuses to publish if the two results differ.

## Usage

Run `meterAI.exe`. The icon appears in the notification area.

- **Hover** for each quota window and its percentage. The tooltip is capped at 63
  characters by the shell, so it carries the figures and drops the countdowns.
- **Click** for the menu above. It reads top to bottom as what is being
  monitored, how fresh it is, and where to go next:
  - the app and its release, then the provider being monitored, what its CLI is
    configured to use, and one row per quota window;
  - _Updated…_ and _Refresh now_, together because the first is the reason for
    the second. On an installation that lives inside WSL, _Refresh now_ is also
    what re-reads the account and the configured model: opening a file in a
    stopped distribution starts it, and that is a cost to pay when you ask for
    it, not every few minutes to redraw a caption;
  - _Providers_, _Settings_ and _Quit_.
- **Providers** lists every monitored subscription by name, one entry each, and
  opens on that provider's own plan, configured model, quota windows — the same
  rows the top level shows for whichever provider is first — and the account
  behind it: name, e-mail, organization. This is the only place a second or
  third provider's own figures appear, since only one provider holds the top
  level at a time; account details live here rather than on the first level
  regardless, which is what keeps an address off the screen until it is asked
  for.
- **Settings** show the value in force beside their name and change the usage
  alerts, the update cadence and the language without a restart. Each change is
  written to the config file before it is applied, so the menu never shows a
  setting that is not on disk.

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
  "providers": {
    "anthropic": { "credentialPath": "" },
    "openai": { "credentialPath": "" }
  },
  "pollInterval": "5m0s",
  "usageAlerts": {
    "warnAtPercent": 75,
    "criticalAtPercent": 90
  },
  "language": "en-US"
}
```

| Field                             | Meaning                                                                                                     |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| `providers.<vendor>.credentialPath` | Explicit path to that vendor's CLI credential file. Empty enables autodiscovery.                          |
| `pollInterval`                    | Polling cadence. Below the safe minimum of 5 minutes it is rejected with a message, not silently corrected. |
| `usageAlerts.warnAtPercent`       | Where a reading starts counting as a warning.                                                                |
| `usageAlerts.criticalAtPercent`   | Where it becomes critical. Must be above `warnAtPercent`.                                                   |
| `language`                        | `en-US` (default) or `pt-BR`.                                                                               |

Everything but `providers` is editable from the _Settings_ submenu, which
writes this same file. Earlier releases stored a single top-level
`credentialPath` (for Anthropic only) and the two thresholds at the top level
of the document; a file in either shape is still read, migrated into the
current shape, and the values in it are kept.

### Usage alerts

_Settings → Usage alerts_ holds the two thresholds, each on its own submenu of
percentages, with the value in force stated beside the name so nothing has to be
opened to read it:

```text
Usage alerts                    75% • 90%
    Warning threshold                 75%
    Critical threshold                90%
```

The warning threshold is always below the critical one. Rather than refuse a
choice, the menu keeps the pair in order for you: setting one past the other
moves the other to the next percentage that keeps them apart, and both rows
update, so a threshold is never a click that does nothing. The two values with
nowhere to go on the far side — the ceiling for a warning, the floor for a
critical — are simply not offered.

A populated `providers.<vendor>.credentialPath` is **authoritative**: if that
path fails, the app reports an error instead of looking elsewhere, since
falling through would mean silently monitoring a different account than the
pinned one.

These thresholds are combined with the vendor's own severity and the more severe
of the two wins; the vendor is never overruled downward. They drive every place
usage is classified, the tray icon's colour included, and a change applies to the
reading already on screen without waiting for the next poll. A config file that
exists but cannot be parsed is an error and is never overwritten, and missing
fields are filled from the defaults, so documents written by earlier releases
keep working.

## What it touches

The whole footprint, and it is a ceiling rather than a default:

- **One process created**: `wsl.exe -l -q`, to list WSL distributions — started
  by its absolute path under `System32`, never through `PATH`, with a fixed
  argument list. Nothing is ever executed inside a distribution; its home
  directories are read over `\\wsl.localhost`.
- **One network destination per configured provider**: each one that vendor's
  own API.
- **Two files written**: `%APPDATA%\meterAI\config.json`, and the tray icon that
  systray hands to Windows as a path in `%TEMP%` (a 4 KiB ICO named after its own
  content hash, at most 97 of them).
- **Nothing else**: no registry key, no startup entry, no service, no scheduled
  task, no dynamic code loading, and no elevation — it carries no manifest
  requesting one, so Windows runs it as invoker by default.

The only secret handled is each provider's OAuth access token, and the
protections are structural: it is redacted by its own type in every rendered
form, its single reveal site is the outbound `Authorization` header, the
refresh token is never carried into memory at all, and no code path writes to
either credential file. Every read from a
file, a socket or a child process is size-bounded, and text coming from documents
the app only reads is neutralized and length-bounded before it reaches a menu
caption.

## Development

```sh
go test -short -race ./...     # offline, deterministic — the default loop
go test -race ./...            # also hits each vendor's live endpoint the host has credentials for
go vet ./... && GOOS=windows go vet ./...
gofmt -l .
go run ./cmd/meterai           # headless: prints each poll to stderr
```

On non-Windows hosts the same `main` compiles and runs headless, printing each
update to stderr, so the whole pipeline — credential discovery, polling, backoff,
formatting — can be exercised where the credentials actually live. Because the
Windows-only files are excluded from a local build, `GOOS=windows go vet ./...`
is part of the loop rather than an afterthought.

[`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs that same loop on
every push and pull request against `master`, and adds the two checks a local
run does not: a Windows runner executing the Windows-tagged tests, and
`govulncheck` over both targets. It cross-compiles the binary alongside them and
attaches it to the run, so a pull request that fails a check still leaves
something to inspect; only once everything passes does it — on `master` alone —
replace the `edge` release with that same artifact.

Contributions should follow [CLAUDE.md](CLAUDE.md), which carries the
architecture, the invariants that span packages, and the review checklist.

## Project layout

```
cmd/meterai/            the binary: wires everything and yields the main goroutine to the tray
internal/quota/         vendor-neutral model, error taxonomy, Provider interface
internal/credential/    locating, reading and caching a CLI credential file, for any vendor
internal/provider/      one directory per vendor, over a shared usageapi client
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

Describe the vendor in `internal/provider/<name>/`: a `usageapi.Endpoint` (its
URL, its extra headers, its renewal command), a decoder from its response onto
`quota.Snapshot`, and a decoder for its CLI's credential file. Then append its
poller to the list the tray is wired with. The HTTP conversation itself —
bearer token, expiry guard, status taxonomy, bounded read — is
`internal/provider/usageapi` and is not written again per vendor. Neither the
core nor the menu changes shape to accommodate a new vendor: the provider list
allocates one entry per configured provider.
[CLAUDE.md §5](CLAUDE.md) states the full contract; the two things easiest to get
wrong are classifying failures correctly, since that drives both retry cadence
and the message shown, and treating every remote field as optional.

## Limitations

- **The quota endpoints are undocumented and unsupported.** They may change shape
  or disappear without notice. The app treats that as an expected condition and
  degrades with an explanation, but no interface that is not a contract can be
  guaranteed.
- **Icon precision.** The gauge is 24 pixel rows tall, roughly four percentage
  points each; exact figures are in the menu and the tooltip.
- **Dependence on each vendor's official CLI.** Without a prior login, and
  without use keeping the token renewed, there is nothing to read for that
  provider.
- **OpenAI shows no account details.** Codex's CLI keeps no local account-profile
  document comparable to Claude's `.claude.json`; the closest equivalent is a
  claim inside the `id_token` JWT in `auth.json`, and decoding a bearer-adjacent
  credential in this process for a cosmetic name/e-mail row is exposure this app
  does not take on. The provider's own submenu simply has no account rows.
- **OpenAI shows no configured model or effort.** That would come from Codex's
  local `config.toml`, mirroring the Preferences row Claude's `settings.json`
  fills. No verified schema for its model/reasoning-effort keys was available
  when this provider was added; the row is omitted rather than guessed at, and
  can be added once a confirmed schema exists.

## Acknowledgements

`github.com/akitaonrails/ai-usagebar` — a Rust implementation for Linux, used as
the initial source for both providers' endpoints and response shapes.

`github.com/mryll/codexbar` — a Bash Waybar widget, used to cross-check the
OpenAI usage endpoint and `auth.json` shape against a second independent
implementation before relying on either alone.

## License

MIT. See [LICENSE](LICENSE).
