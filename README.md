# meerkat-agent

The open-source monitoring agent for **Meerkat**, a server monitor for iOS.

You install this small program **on a server you own**. It reads the machine's
health — CPU, memory, disk, network — and exposes it as a single JSON document
that the Meerkat app reads. That's the whole job.

> **Don't trust us — read it.** This agent is the trust foundation of the product.
> It is intentionally tiny, dependency-free, and read-only, so you (or your AI) can
> audit every line before running it on your box. Your data never passes through
> our servers — the app talks directly to *your* agent.

---

## Design principles

- **Zero dependencies.** Pure Go standard library. Nothing to vet but the code in
  this repo.
- **Read-only.** The agent only *reads* the machine. It opens no shells, runs no
  remote commands, holds no credentials, and writes nothing to your system. The
  only network it originates is reachability probes to hostnames **your own
  web-server config already serves** (see *What the agent reads*, below).
- **No configuration, no input.** There is nothing to configure and the agent
  accepts no input from the request. It discovers what's running by reading what
  is already on the box, so an auditor can reason about its entire behaviour from
  the source alone.
- **A single static binary.** One build runs across distributions — glibc or musl
  (Alpine), amd64 or arm64 — with no runtime to install.
- **One contract, every platform.** The JSON shape is identical on every OS and
  mirrors the Meerkat app's data model.

---

## Status

**Linux: feature-complete for the app's data model (v0.2.x).** Every field the
Meerkat app renders is now produced on Linux. macOS remains a limited fallback.

| Area | Linux | macOS |
|---|---|---|
| Host (name, OS, kernel, arch, uptime) | ✅ | ✅ (no uptime) |
| Disk usage | ✅ | ✅ |
| CPU usage | ✅ | ⬜ stubbed |
| Memory usage | ✅ | ⬜ stubbed |
| Network throughput | ✅ | ⬜ stubbed |
| Containers (Docker) | ✅ | ⬜ planned |
| Databases (engine, size, status) | ✅ | ⬜ planned |
| Endpoints (auto-discovered + probed) | ✅ | ⬜ planned |
| App-groups (components clustered by name) | ✅ | ⬜ planned |

Linux is the primary target and reads directly from `/proc`, `statfs`, the Docker
socket, and the local web-server config. macOS is supported as a limited fallback
(host + disk are real; metrics report `0`, discovery is empty, and the platform is
reported as `darwin (limited)`) — a full Darwin backend is future work.

---

## What the agent reads (and why it's safe)

The agent takes **no configuration and no request input**. It discovers
everything by reading sources already present on the machine, all read-only:

| Snapshot field | Source on the box | Notes |
|---|---|---|
| host / cpu / memory / disk / network | `/proc/*`, `statfs`, `uname` | Kernel pseudo-files; rates need two samples. |
| containers | `GET` on `/var/run/docker.sock` | Read-only Docker API. CPU%/mem from two stats frames. Skipped if Docker is absent or the socket isn't permitted. |
| databases | process table (`/proc/*/comm`) + on-disk data dirs | Detects PostgreSQL, MySQL/MariaDB, Redis, MongoDB. Size is the data directory's on-disk size. **No connection, no credentials**, so it reports what's visible on disk (per-schema sizes for MySQL/MariaDB; cluster-level size for the others). |
| endpoints | nginx / Apache / Caddy site config | The agent probes only the hostnames **your own server is already configured to serve** — never an attacker-supplied or arbitrary URL. |
| groups | synthesized | The discovered containers/databases/endpoints are clustered by a shared name root into "apps". Pure local computation. |

The only outbound traffic the agent originates is the endpoint reachability
probe, and only ever to hostnames found in this machine's own web-server config.
Everything else is local reads. There are no shells, no credentials, and no
writes.

---

## Install

The agent is published to a single channel: an **apt repository for Ubuntu**.
Add the repository once, then install and upgrade with `apt` like any other
package. (The repository is hosted as static, signed files on GitHub Pages from
this same GitHub account — there is no other distribution channel.)

```sh
# add the signing key and repository (one time), then install
# (exact key URL / repo URL are filled in when the repo goes live)
sudo apt update
sudo apt install meerkat-agent
```

Updates arrive through the normal `sudo apt update && sudo apt upgrade`.

### Build it yourself (open source)

The agent is open source — clone the repo, read every line, and build your own
binary if you prefer not to trust the published package:

```sh
git clone https://github.com/AndiOliverIon/meerkat-agent.git
cd meerkat-agent
go build -o bin/meerkat-agent ./cmd/meerkat-agent   # Go 1.23+
```

This is for auditing and self-builds; the supported install path for end users
is the apt repository above.

---

## Usage

```
meerkat-agent once             collect one snapshot and print JSON
meerkat-agent serve [--addr]   serve the snapshot over HTTP (default :8765)
meerkat-agent version          print the agent version
```

### One-shot (good for testing or cron)

```sh
meerkat-agent once
```

### Serve over HTTP

```sh
meerkat-agent serve --addr :8765
```

| Method & path | Returns |
|---|---|
| `GET /v1/status` | the full snapshot (JSON) |
| `GET /healthz`   | `{"status":"ok","agent":"<version>"}` |

This is the **direct app ⇄ agent** path: the Meerkat app connects to this endpoint
on your server. Expose it only to networks/devices you trust.

> **Note on rates:** CPU and network are derived from monotonic counters, so they
> need two samples. The *first* read after start reports `0` for those until a
> second sample exists. In `serve` mode the collector is long-lived, so subsequent
> reads are accurate.

---

## The JSON contract

`GET /v1/status` (and `once`) return a `Snapshot`:

```json
{
  "agentVersion": "0.1.0",
  "collectedAt": "2026-06-13T13:21:26Z",
  "host":    { "name": "", "os": "", "kernel": "", "arch": "", "platform": "", "uptimeSeconds": 0 },
  "cpu":     { "used": 0, "total": 100, "unit": "%",  "percent": 0 },
  "memory":  { "used": 0, "total": 0,   "unit": "GB", "percent": 0 },
  "disk":    { "used": 0, "total": 0,   "unit": "GB", "percent": 0 },
  "network": { "interface": "", "rxMbps": 0, "txMbps": 0 },
  "groups": [], "containers": [], "databases": [], "endpoints": []
}
```

The field definitions live in [`internal/model/model.go`](internal/model/model.go),
which is the canonical, commented source of truth for the contract.

---

## Project layout

```
cmd/meerkat-agent/   CLI entrypoint (once | serve | version)
internal/model/      JSON contract (Snapshot, Host, Metric, …)
internal/collect/    portable collector + per-OS backends (build-tagged)
  collect.go         portable: samples counters, derives rates, assembles
  groups.go          portable: clusters discovered components into app-groups
  sys_linux.go       Linux metrics (host/cpu/mem/disk/net)
  sys_darwin.go      Darwin metrics (limited fallback)
  discover_linux.go  Linux discovery (containers/databases/endpoints)
  discover_darwin.go Darwin discovery stubs
internal/server/     HTTP server (/v1/status, /healthz)
```

Per-OS code is selected at compile time with Go build tags (`*_linux.go`,
`*_darwin.go`), so the binary contains only the backend for its target.

---

## Roadmap

- ~~Real resource discovery on Linux (apps, containers, databases, endpoints)~~ ✅ done
- Package as a signed `.deb` and publish to an apt repository for Ubuntu
  (the single supported install channel) with a systemd service
- Optional short-TTL cache for discovery so frequent polls don't re-probe every call
- Full macOS backend (CPU, memory, network) + macOS discovery (launchd, Docker Desktop)

> Other package formats (RHEL `.rpm`, Alpine `.apk`, Arch, Homebrew) and other
> install methods are explicitly out of scope for now — Ubuntu via apt is the
> only published target.

---

## License

To be finalized before the first tagged release.
