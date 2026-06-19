# meerkat-agent

The open-source monitoring agent for **Meerkat**, a server monitor for iOS.

You install this small program **on a server you own**. It reads supported
machine facts and discovered resources, then exposes one HTTPS JSON document
that the Meerkat app reads in Free/direct mode.

> **Don't trust us blindly. Read it.** This agent is the trust foundation of the
> product. It is intentionally small, dependency-light, and read-only: no remote
> shell, no UI-triggered server actions, and no credentials sent from the app.

---

## Current stage

This repo is being prepared for the **Free/direct v1 flow**:

1. User installs `meerkat-agent` from our signed GitHub apt repository.
2. Agent generates a self-signed TLS certificate and bearer token.
3. Agent prints one enrollment string for the iOS app.
4. iOS app connects directly to `https://server:8765/v1/status`.
5. App pins/trusts the agent certificate and uses the token for `/v1/status`.

Packaging and GitHub Actions publishing are wired through a signed apt
repository. Public publishing requires the permanent Meerkat apt signing key in
GitHub Actions secrets.

---

## Design principles

- **Read-only for v1.** The agent observes and reports. It does not restart
  containers, run commands, apply updates, or mutate the user's VPS.
- **Direct Free mode.** The Free app talks directly to the installed agent. No
  Meerkat account or relay is required for Free.
- **Nullable honesty.** Values that cannot be obtained are JSON `null`, never
  fake zeroes. Empty arrays mean the source was read and nothing was found.
- **No app-provided secrets.** The iOS app should not store database passwords.
  If deeper database discovery needs credentials later, they must be configured
  on the VPS side for the agent.
- **No magic discovery.** The agent reports what it can read from supported
  local sources.

---

## What the agent reads

| Area | Current source | Notes |
| --- | --- | --- |
| Host | hostname, OS files, kernel, arch, uptime | Always included where possible. |
| System metrics | `/proc`, `statfs`, `/proc/loadavg` | CPU, memory, disk, and load average. |
| Docker containers | Docker API over `/var/run/docker.sock` | State, health, restart count, ports, timestamps, exit code, OOM flag, error text. Unknown lifecycle values are `null`, not fake zeroes. |
| PostgreSQL | local `psql` when available, plus known data dirs | Reports per-database names/sizes when local read-only access works; otherwise reports readable cluster evidence. |
| MSSQL in Docker | Docker metadata + mounted data files + optional SQL credentials | Reports mounted database files when readable; optional SQL credentials can improve database inventory and SQL memory-pressure signals. |
| Endpoints | nginx / Apache / Caddy config | Names only. The agent does not probe endpoints in Free v1. |

## Important known gaps

- PostgreSQL per-database names/sizes depend on local `psql` access. Without
  that, the agent reports only readable cluster evidence.
- The permanent apt signing key must be generated and backed up before public
  release. See [`docs/apt-signing-key.md`](docs/apt-signing-key.md).
- Pro relay mode has a supervised service and one-time enrollment code flow.
  Relay credentials are still development-grade and must be hardened before
  production telemetry.

---

## Install

The supported install path is an **apt repository hosted from our GitHub
account**, not Ubuntu's central repository. Users explicitly trust our key, add
our repository, then install with apt.

```sh
curl -fsSL https://andioliverion.github.io/meerkat-agent/apt/key.gpg \
  | sudo gpg --dearmor -o /usr/share/keyrings/meerkat-agent.gpg

echo "deb [signed-by=/usr/share/keyrings/meerkat-agent.gpg] https://andioliverion.github.io/meerkat-agent/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/meerkat-agent.list

sudo apt update
sudo apt install meerkat-agent
```

Updates will arrive through normal apt once the repository exists:

```sh
sudo apt update
sudo apt upgrade
```

---

## Usage

```sh
meerkat-agent once                      collect one snapshot and print JSON
meerkat-agent serve [--addr][--dir]     serve HTTPS API, default :8765
meerkat-agent relay [--dir] [--backend-url URL --server-id ID --relay-token TOKEN]
                                        push snapshots to Meerkat relay
meerkat-agent gen-cert [--dir]          generate TLS cert/key if absent
meerkat-agent gen-token [--dir]         generate bearer token if absent
meerkat-agent rotate-token [--dir]      replace token and print enrollment
meerkat-agent rotate-cert [--dir]       replace TLS cert/key and print enrollment
meerkat-agent fingerprint [--dir]       print TLS cert fingerprint
meerkat-agent enroll [--dir][--addr]    print app enrollment details
meerkat-agent config remove-mssql [--dir] <container>
meerkat-agent config relay set --enrollment-code CODE [--dir]
meerkat-agent config relay set --backend-url URL --server-id ID --relay-token TOKEN
meerkat-agent config relay show [--dir]
meerkat-agent config relay remove [--dir]
meerkat-agent version                   print version
```

### Serve

```sh
meerkat-agent serve --addr :8765
```

| Method & path | Auth | Returns |
| --- | --- | --- |
| `GET /healthz` | none | liveness only |
| `GET /v1/status` | bearer token | full snapshot JSON |
| `POST /v1/config/mssql` | bearer token | tests and stores optional MSSQL inventory credentials |
| `DELETE /v1/config/mssql/{container}` | bearer token | removes stored MSSQL inventory credentials |

`/v1/status` is served over HTTPS with the agent's self-signed certificate.

### Relay enrollment

For Pro relay mode, generate the server-scoped command from the Meerkat app and
run it on the VPS:

```sh
sudo meerkat-agent config relay set --enrollment-code CODE
sudo systemctl enable meerkat-agent-relay
sudo systemctl restart meerkat-agent-relay
```

Enrollment codes are short-lived and single-use. If the command fails after the
backend accepts the code, create a fresh command in the app and run it again.

### Relay

Relay mode pushes snapshots outbound to the Meerkat backend. This is the Pro
path: the VPS agent remains the source of truth, and the relay stores the latest
snapshot for the app to read.

Configure relay identity on the VPS only for development or recovery. The app
generated enrollment command is preferred because it returns a server-scoped
relay token.

```sh
sudo meerkat-agent config relay set \
  --backend-url https://api.meerkat.tnisoft.ro \
  --server-id SERVER_ID \
  --relay-token RELAY_TOKEN
```

Start the supervised relay service:

```sh
sudo systemctl enable meerkat-agent-relay
sudo systemctl restart meerkat-agent-relay
```

The package installs `meerkat-agent-relay.service` separately from
`meerkat-agent.service`. The direct service can continue serving Free/direct
mode on port `8765`, while the relay service continuously pushes snapshots.
The relay service starts only when `/var/lib/meerkat-agent/relay.json` exists
and uses `Restart=always` so systemd brings it back after failures.

For development or one-off tests, relay values can be passed as flags instead
of saving config:

```sh
meerkat-agent relay \
  --backend-url http://127.0.0.1:5281 \
  --server-id SERVER_ID \
  --relay-token RELAY_TOKEN
```

### Optional MSSQL inventory

By default the agent can detect an MSSQL Docker container, but it cannot list
databases inside SQL Server without SQL credentials. If the user chooses to
enable deeper discovery, the app can send a limited SQL username/password to
`POST /v1/config/mssql`.

The agent then:

- tests the credentials with a read-only metadata query;
- stores them locally on the VPS in `/var/lib/meerkat-agent`, mode `0600`;
- uses them only to list database names/sizes and read SQL Server memory-pressure DMVs;
- never returns the password from the API.

Use a dedicated read-only monitoring login, not an administrator password.
This is agent configuration, not a server action: the app does not restart
services, mutate databases, or execute user workload changes.
SQL memory-pressure signals may require the monitoring login to have
`VIEW SERVER STATE`; without that permission the agent omits `sqlServers`
rather than guessing.

To revoke stored MSSQL inventory credentials later:

```sh
sudo meerkat-agent config remove-mssql <container>
```

### Enrollment

```sh
meerkat-agent enroll --addr your.server.example.com:8765
```

The command prints:

- address
- certificate fingerprint
- bearer token
- one-paste line for the iOS app
- base64 JSON code for future parser support

If a token is exposed or needs refreshing:

```sh
sudo meerkat-agent rotate-token
```

This replaces only the bearer token. The TLS certificate and fingerprint remain
unchanged, so the app can recover without forcing a full certificate reset.
The running service reloads the token from disk during authentication, so old
tokens are rejected immediately after rotation without restarting the service.

If the TLS certificate itself must be replaced:

```sh
sudo meerkat-agent rotate-cert --addr your.server.example.com:8765
sudo systemctl restart meerkat-agent
```

This keeps the bearer token, creates a fresh Apple-compliant self-signed
certificate, prints a new enrollment string, and changes the certificate
fingerprint the app must trust.

---

## JSON contract

`GET /v1/status` returns a `Snapshot`. The canonical source is
[`internal/model/model.go`](internal/model/model.go).

High-level shape:

```json
{
  "agentVersion": "0.0.0-dev",
  "collectedAt": "2026-06-14T09:00:00Z",
  "host": { "name": "vps1", "os": "Ubuntu 24.04 LTS", "kernel": "...", "arch": "amd64", "platform": "linux", "uptimeSeconds": 12345 },
  "cpu": { "used": 12.4, "total": 100, "unit": "%", "percent": 12.4 },
  "memory": { "used": 1.7, "total": 4.0, "unit": "GB", "percent": 42.5 },
  "disk": { "used": 18.6, "total": 80.0, "unit": "GB", "percent": 23.3 },
  "load": { "one": 0.1, "five": 0.2, "fifteen": 0.2 },
  "containers": [],
  "databases": [],
  "sqlServers": [],
  "endpoints": []
}
```

`null` means not obtained. `[]` means obtained and empty.

---

## Project layout

```text
cmd/meerkat-agent/   CLI entrypoint
internal/model/      JSON contract
internal/collect/    platform collectors and discovery
internal/identity/   TLS cert, fingerprint, bearer token
internal/server/     HTTPS API server
```

---

## License

To be finalized before the first tagged release.
