# amnezia-mesh-go

Go port of [`amnezia-mesh`](https://github.com/alexpozdnyakof/amnezia-mesh) —
self-hosted WireGuard mesh with AmneziaWG traffic obfuscation, MagicDNS and
Tailscale-style exit/mesh profiles, as a single static binary with no runtime
dependencies beyond Docker.

The Python implementation is the frozen reference; this port is the canonical
implementation going forward. **Parity is defined as byte-identical
artifacts**: configs, peer databases, JSON output and CLI behaviour are
compared byte-for-byte against the Python version in CI-style harness runs
(see `scripts/parity.sh`).

## Install

Pre-built binaries (darwin/linux, amd64/arm64) are published with each
[release](https://github.com/alexpozdnyakof/amnezia-mesh-go/releases):

```sh
brew tap alexpozdnyakof/tap
brew install --cask alexpozdnyakof/tap/awg-mesh

awg-mesh --version
```

Or build from source:

```sh
go build ./cmd/awg-mesh
```

## Usage

Identical to the Python version — same commands, same flags, same JSON
protocol, same on-disk layout (env, `config/awg0.conf`, `config/peers.json`,
rendered peer configs):

```sh
awg-mesh init --host YOUR_IP_OR_DOMAIN --port 443
awg-mesh peer add laptop     # → vpn:// profiles + .conf for the Amnezia app
awg-mesh peer list --json
awg-mesh doctor              # post-install self-checks
awg-mesh monitoring install  # optional: cron stats + watchdog
```

Every command supports `--json` (single JSON object on stdout, non-zero exit +
`{"ok": false, "error": "..."}` on failure) and `AWG_MESH_*` env-var
overrides; fully non-interactive via `AWG_MESH_NONINTERACTIVE=1`.

## Layout

| Path | Contents |
|---|---|
| `cmd/awg-mesh` | CLI entrypoint |
| `internal/envfile` | `.env` / env-file parsing (order-preserving) |
| `internal/peers` | peers.json store with flock (POSIX `flock` via x/sys) |
| `internal/render` | awg0.conf / dnsmasq / peer config rendering |
| `internal/ojson` | order-preserving JSON with CPython-compatible escaping |
| `internal/profile` | `vpn://` codec (Amnezia import links) |
| `internal/keygen` | key generation via `docker run --entrypoint awg` |
| `internal/compose` | docker compose project helpers |
| `internal/doctor` | self-checks (docker, port, ip_forward, MagicDNS) |
| `internal/adopt` | adopt a running AWG deployment |
| `internal/mesh` | shared state/config model |
| `internal/cli` | command dispatch, `--json` output, exit codes |
| `scripts/parity.sh` | byte-level parity harness vs the Python reference |
| `docs/parity-live.md` | side-by-side live test stand procedure |

Dependencies: Go standard library + `golang.org/x/sys` (flock only).

## Development

```sh
make test      # go test ./...
make parity    # run the Python↔Go parity harness (needs AWG_MESH_PY checkout)
make snapshot  # local goreleaser snapshot build
```

See `PLAN.md` for the staged development plan and the Python porting scope.

## License

Same as the parent project — see `LICENSE`.
