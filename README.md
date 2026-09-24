# Fleet Connector

Fleet Connector packages the [ngrok](https://ngrok.com) agent as installable,
platform-native software instead of a script — an MSI + Windows service on
Windows, a `.deb`/`.rpm` + systemd unit on Linux. It's a reference
implementation built directly on
[`golang.ngrok.com/ngrok/v2`](https://pkg.go.dev/golang.ngrok.com/ngrok/v2),
meant to be forked and rebranded by anyone who needs to roll ngrok out across
a fleet of on-prem machines without hand-editing YAML at every site.

**What it gives you:**

- **A guided setup wizard** — a local, one-time web UI (`http://127.0.0.1:<port>`)
  that a non-technical installer fills in instead of hand-authoring a config
  file. It validates the config by actually connecting before writing
  anything to disk.
- **A `gen-config` CLI** for ops to pre-generate a config centrally (e.g. for
  RMM/Intune/SCCM push, or a scripted Linux rollout) — no wizard involved.
- **An `edit-config` CLI** for safely hand-editing an already-deployed config
  later (opens it in your `$EDITOR`/Notepad, validates before saving —
  the same pattern as `visudo`/`crontab -e`).
- **Runs as a real background service** on both platforms — auto-start,
  crash-restart, and remote stop/restart from the [ngrok
  dashboard](https://dashboard.ngrok.com), for free, via the SDK's RPC
  handler.
- **Structured logging** to Windows Event Log + a rotating file on Windows,
  and journald + a rotating file on Linux.

No ngrok credentials, API keys, or authtokens are stored in this repository —
every config file this tool produces is written locally, at install/setup
time, from a token you supply yourself (see [Getting an authtoken](#getting-an-authtoken)
below).

## Requirements

- [Go 1.26+](https://go.dev/dl/) to build
- An [ngrok account](https://dashboard.ngrok.com/signup) (free tier works)
- **Windows only:** the [WiX Toolset v5 CLI](https://wixtoolset.org/) if you
  want to build the MSI installer (not required just to run the binary
  directly)
- **Linux only:** [`nfpm`](https://nfpm.goreleaser.com/) if you want to build
  a `.deb`/`.rpm` package (not required for the tarball path or running the
  binary directly)

### Getting an authtoken

Sign up at [ngrok.com](https://dashboard.ngrok.com/signup), then grab your
authtoken from the [dashboard](https://dashboard.ngrok.com/get-started/your-authtoken).
You'll paste this into the setup wizard, or pass it to `gen-config`/an
install-time environment variable — never into a config file checked into
source control.

### Get the code

```sh
git clone https://github.com/ishanj12/fleet-connector.git
cd fleet-connector
```

Every command below is run from inside this directory.

## Quick start (either OS, no packaging)

The fastest way to try it out — build the binary and run it in the
foreground:

```sh
go build -o fleetconnect ./cmd/fleetconnect
./fleetconnect
```

On first run, since no config exists yet, it starts the setup wizard and
prints a URL (also logged to stderr) — open it in a browser, paste your
authtoken, add at least one endpoint (e.g. `upstream: localhost:8080`),
save, and confirm. It writes `config.yaml` in the current directory and
starts forwarding immediately. Ctrl+C to stop.

Once `config.yaml` exists, every future run of `./fleetconnect` connects
directly — the wizard only ever runs once, against a missing config.

To skip the wizard entirely and generate a config non-interactively:

```sh
./fleetconnect gen-config \
  --description "my-first-install" \
  --upstream localhost:8080 \
  --authtoken "<your authtoken>"
./fleetconnect
```

## Configuring

Three ways to produce or change `config.yaml`, at any point:

| Tool | When to use it |
|---|---|
| **Setup wizard** (browser UI) | First-time, on-site, non-technical installer — no config exists yet |
| **`fleetconnect gen-config [flags]`** | Ops pre-generates config centrally before deployment — see [below](#gen-config-in-detail) |
| **`fleetconnect edit-config [path]`** | Change a field on an already-deployed install — opens the real file in `$EDITOR`/`$VISUAL` (falls back to `notepad.exe` on Windows, `nano`/`vi` on Linux), validates before saving |

**Important:** after any config change, the running service needs a real
restart to pick it up — `systemctl restart fleetconnect` on Linux, or
restarting the Windows service. A dashboard-initiated restart from ngrok's
UI does **not** re-read the file from disk; it only recycles the existing
connection.

### `gen-config` in detail

`--description` is the only always-required flag — it's both a human-readable
label for this install (shown in the ngrok dashboard) and, if minting a fresh
credential (see below), the label attached to that credential too.

**One endpoint** — use the shorthand flags:

```sh
./fleetconnect gen-config \
  --description "store-042" \
  --upstream localhost:8080 \
  --url https://store-042.example.com \
  --authtoken "<your authtoken>"
```

`--upstream` is required whenever you use the shorthand; `--url` is optional
(blank means ngrok assigns a random HTTPS URL) and `--name` is an optional
label for the endpoint itself (not the whole install).

**More than one endpoint** — repeat `--endpoint` instead, once per tunnel,
as a comma-separated `key=value` list. This is mutually exclusive with the
`--name`/`--upstream`/`--url` shorthand above:

```sh
./fleetconnect gen-config \
  --description "store-042" \
  --authtoken "<your authtoken>" \
  --endpoint "name=pos-1,upstream.url=localhost:8080,url=https://pos-1.example.com" \
  --endpoint "name=pos-2,upstream.url=localhost:8081"
```

Each `--endpoint` value only supports three keys — `name`, `upstream.url`
(required), and `url` (optional). Anything beyond that (bindings, traffic
policy, agent TLS termination) means hand-editing the generated file
afterward, or using the wizard instead, which exposes the full schema.

**Other useful flags:**

| Flag | Purpose |
|---|---|
| `--path` | Where to write the file (defaults to `config.yaml` in the current directory — use the platform's fixed path, e.g. `/etc/fleetconnect/config.yaml`, when pre-staging for an install) |
| `--metadata` | Opaque session metadata (distinct from `--description`) |
| `--log-level` | `debug`, `info` (default), `warn`, or `error` |
| `--authtoken` | An existing authtoken: a literal value, `env:VARNAME`, or `file:PATH` (reads a pre-staged credentials file — nothing but the *path* needs to touch a command line or CI variable this way) |
| `--connect-url` | Custom/dedicated/self-hosted ngrok connect endpoint (blank uses ngrok's public one) |
| `--connect-ca-cert-file` | PEM CA bundle to trust for `--connect-url`, if it needs one |
| `--proxy-url` | Outbound HTTP/SOCKS proxy to route the agent's own connection through — relevant for corporate networks that force traffic through one |
| `--heartbeat-interval` | Connection heartbeat cadence, e.g. `30s` |
| `--heartbeat-tolerance` | How long a missed heartbeat is tolerated before the connection is considered disconnected, e.g. `1m` — useful for flaky/high-latency links |

These same five settings are also available in the setup wizard, under
"Advanced connection settings."

Omitting `--authtoken` entirely mints a brand-new, uniquely revocable
credential via ngrok's Credentials API instead — set `FLEETCONNECT_NGROK_API_KEY`
(or `FLEETCONNECT_NGROK_API_KEY_FILE`) to your ngrok **API key** (not an
agent authtoken — this is the account-scoped dashboard key) for this to
work:

```sh
export FLEETCONNECT_NGROK_API_KEY="<your ngrok API key>"
./fleetconnect gen-config --description "store-042" --upstream localhost:8080
```

## Remote updates (Windows only)

fleet-connector can update itself in place when triggered remotely — via
ngrok's dashboard, or the Tunnel Sessions API's `update` operation — instead
of requiring physical or remote-desktop access to every machine for every
release. Disabled by default; enable it with two required flags:

```sh
./fleetconnect gen-config \
  --description "store-042" \
  --upstream localhost:8080 \
  --authtoken "<your authtoken>" \
  --update-source-url "https://updates.example.com/latest/fleetconnect.msi" \
  --update-signer-thumbprint "AABBCCDDEEFF00112233445566778899AABBCCDD"
```

(`update_source_url`/`update_signer_thumbprint` in `config.yaml`, or the
setup wizard's "Advanced connection settings," work the same way.)

**How it works:** the RPC arrives over the agent's existing connection (no
new inbound/outbound rule needed) → the agent downloads whatever
`--update-source-url` points at → verifies its signature → launches it
silently, detached from the current process, since the installer's own
upgrade logic is about to stop the very service running this code.
Windows-only today — the RPC itself arrives on any platform, but the
download/verify/launch mechanism assumes an MSI and Windows Authenticode
signing (see `internal/update`).

**`--update-signer-thumbprint` is not optional decoration — it's the actual
safety gate.** `gen-config` and `config.Validate` both refuse to enable the
feature without it. This matters because `--update-source-url` will often
end up being a plain, unauthenticated location in practice: an unattended
agent can't complete an interactive SSO/MFA login the way a human fetching
the same file could, so access control on the download itself usually isn't
an option. The thumbprint check — confirming the file is signed by *this
specific* certificate, not just *some* validly-issued one — is what makes
that safe. Get the value by running
`(Get-AuthenticodeSignature path).SignerCertificate.Thumbprint` in
PowerShell against your own signed installer.

**Use one signing certificate, permanently.** Don't rotate it casually.
Every release still needs to match `update_signer_thumbprint`, so switching
certificates means updating every deployed config alongside the new
release — and separately, Windows' own reputation systems (SmartScreen and
similar) treat a certificate switch as starting over from zero trust,
independent of anything to do with this feature. A renewed certificate
(same identity, continuous history) keeps accumulated reputation; a brand
new one doesn't.

**One thing this can't fix, no matter how it's configured:** EDR/antivirus
products commonly detect ngrok usage by watching for connections to ngrok's
own infrastructure (domains, protocol-level fingerprints), not just by
recognizing the stock `ngrok.exe` binary. Building on the SDK avoids the
latter, but not the former — that risk is inherent to using ngrok's cloud
at all, and no code change here removes it. The actual mitigation is
getting your signing certificate allowlisted with each deployment's IT/EDR
team ahead of time, not a configuration setting.

## Installing on Windows

The commands below are PowerShell, run **on the Windows machine** — clone
the repo there first (same command as [above](#get-the-code), it works
identically in PowerShell):

```powershell
git clone https://github.com/ishanj12/fleet-connector.git
cd fleet-connector
```

### 1. Build the binary

```powershell
$env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -o fleetconnect.exe ./cmd/fleetconnect
```

(`$env:GOOS`/`$env:GOARCH` are redundant if you're already building natively
on Windows/amd64 — Go defaults to your current platform. They're here so
this same command also works if you instead cross-compile from a Mac/Linux
box and copy the resulting `fleetconnect.exe` over for step 2.)

### 2. Build the MSI

```powershell
dotnet tool install --global wix --version "5.*"
wix extension add WixToolset.Util.wixext/5.0.2
wix build installer\product.wxs -ext WixToolset.Util.wixext/5.0.2 -arch x64 -d FleetConnectExeSource=fleetconnect.exe -out FleetConnector.msi
```

Run both `wix` commands from the same directory — `wix extension add`
caches the extension relative to your current working directory, not
globally.

### 3. Install

Plain interactive install (launches the wizard on first service start,
since no config is pre-staged):

```powershell
msiexec /i FleetConnector.msi
```

Silent install, letting the installer fill in config from MSI properties
(no wizard, no pre-staged file) — run from `cmd.exe`, or prefix with `--%`
if using PowerShell to avoid its argument quoting mangling the values:

```powershell
msiexec --% /i FleetConnector.msi /quiet FLEETCONNECT_INSTALL_DESCRIPTION="store-042" FLEETCONNECT_INSTALL_UPSTREAM="localhost:8080" FLEETCONNECT_CREDFILE="C:\path\to\authtoken.txt"
```

(`FLEETCONNECT_CREDFILE` should point at a plain text file containing only
the authtoken — never pass the token itself as a property, it would land in
plaintext in the install log.)

Or simplest for a scripted rollout: run `gen-config` yourself first and
drop the resulting file at `%ProgramData%\FleetConnector\config.yaml` before
installing — the service will find it already there and connect
immediately with zero interaction.

### 4. Verify

```powershell
Get-Service FleetConnector
Get-EventLog -LogName Application -Source FleetConnector -Newest 10
```

### Uninstalling

```powershell
msiexec /x FleetConnector.msi
```

`config.yaml` is deliberately left behind at
`%ProgramData%\FleetConnector\config.yaml` (it holds a live credential) —
revoke the credential via ngrok's Credentials API as part of
decommissioning, then delete the file yourself if you want it gone.

## Installing on Linux

Run on the target Linux machine (clone the repo there first, [as above](#get-the-code)) —
or cross-compile elsewhere and copy `fleetconnect` + `installer/linux/` over.

### 1. Build the binary

```sh
GOOS=linux GOARCH=amd64 go build -o fleetconnect ./cmd/fleetconnect   # or GOARCH=arm64
```

### Option A — `.deb`/`.rpm` package (systemd required)

```sh
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest
# edit installer/linux/nfpm.yaml's `arch:` field to match your target (amd64/arm64)
nfpm package --config installer/linux/nfpm.yaml --packager deb --target dist/
sudo dpkg -i dist/fleetconnect_*.deb
```

With nothing pre-staged, the service starts and runs the setup wizard —
check the log for its URL:

```sh
sudo journalctl -u fleetconnect -f
```

If the box is headless (no browser), the log includes an SSH port-forward
hint: `ssh -L <port>:127.0.0.1:<port> user@host`, then open the URL from
your own machine's browser.

To skip the wizard, either pre-stage a config before installing:

```sh
./fleetconnect gen-config --description "store-042" --upstream localhost:8080 --authtoken "<your authtoken>" --path /tmp/config.yaml
sudo mkdir -p /etc/fleetconnect
sudo cp /tmp/config.yaml /etc/fleetconnect/config.yaml
sudo dpkg -i dist/fleetconnect_*.deb
```

or supply install-time environment variables so `postinstall.sh` runs
`gen-config` for you:

```sh
sudo FLEETCONNECT_INSTALL_DESCRIPTION="store-042" FLEETCONNECT_INSTALL_UPSTREAM="localhost:8080" FLEETCONNECT_CREDFILE=/path/to/authtoken.txt dpkg -i dist/fleetconnect_*.deb
```

### Option B — tarball (no systemd, or no package manager)

```sh
mkdir fleetconnect-release
cp fleetconnect installer/linux/fleetconnect.service installer/linux/tarball/install.sh installer/linux/tarball/uninstall.sh fleetconnect-release/
cd fleetconnect-release
sudo ./install.sh
```

Reads the same `FLEETCONNECT_INSTALL_*`/`FLEETCONNECT_CREDFILE` environment
variables as the `.deb` path. Detects systemd and wires up the unit
automatically if present; otherwise installs the binary/config and prints
instructions for your own supervisor.

### Verify

```sh
sudo systemctl status fleetconnect
sudo journalctl -u fleetconnect -n 50
```

### Uninstalling

```sh
sudo dpkg -r fleetconnect          # .deb path
# or
sudo ./uninstall.sh                # tarball path
```

Both leave `/etc/fleetconnect/config.yaml` in place — same reasoning as
Windows above.

## Repository layout

```
cmd/fleetconnect/      entrypoint, gen-config, edit-config, Windows/Linux service wrappers
internal/agent/        App lifecycle (Start/Stop/Status)
internal/config/       Config schema, validation, the local file-based Source
internal/credentials/  Pluggable credential provider (StaticProvider by default)
internal/tunnel/       Manager — the supervisory reconnect loop wrapping the ngrok SDK
internal/update/       Remote self-update: download, verify (Authenticode), launch (Windows only)
internal/wizard/       The local setup wizard's web UI
internal/logging/      Structured logging, per-platform sinks
installer/             MSI (WiX) and Linux (.deb/.rpm/tarball) packaging
```

## Running the tests

```sh
go test ./...
```

Tests use hand-written fakes for the ngrok SDK's `Agent`/`EndpointForwarder`
interfaces — no real network or ngrok account needed to run the suite.

## License

MIT — see [LICENSE](LICENSE).
