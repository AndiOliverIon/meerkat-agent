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

Packaging and GitHub Actions publishing are the next stage. The apt URLs below
are placeholders until that publishing flow exists.

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
| Docker containers | Docker API over `/var/run/docker.sock` | State, health, restart count, ports, timestamps, exit code, OOM flag, error text. |
| PostgreSQL | process table + known data dirs | Reports a PostgreSQL cluster entry and on-disk size when readable. |
| MSSQL in Docker | Docker container name/image | Identifies MSSQL container candidates; per-database inventory is not solved yet. |
| Endpoints | nginx / Apache / Caddy config | Names only. The agent does not probe endpoints in Free v1. |

## Important known gaps

- Per-database **names and sizes** inside MSSQL containers need a reliable
  read-only strategy.
- Per-database PostgreSQL names/sizes also need a reliable strategy beyond
  cluster-level filesystem sizing.
- The signed apt repository and package publishing flow still need to be built.
- Pro relay mode is not implemented in this repo yet.

---

## Install

The supported install path is an **apt repository hosted from our GitHub
account**, not Ubuntu's central repository. Users explicitly trust our key, add
our repository, then install with apt.

```sh
# placeholder until the repo goes live
curl -fsSL https://andioliverion.github.io/meerkat-agent/apt/key.gpg \
  | sudo gpg --dearmor -o /usr/share/keyrings/meerkat-agent.gpg

# placeholder repo line until packaging is published
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
meerkat-agent gen-cert [--dir]          generate TLS cert/key if absent
meerkat-agent gen-token [--dir]         generate bearer token if absent
meerkat-agent rotate-token [--dir]      replace token and print enrollment
meerkat-agent fingerprint [--dir]       print TLS cert fingerprint
meerkat-agent enroll [--dir][--addr]    print app enrollment details
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

`/v1/status` is served over HTTPS with the agent's self-signed certificate.

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
