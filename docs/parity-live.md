# Live parity checks on a parallel stand (server-side)

`make parity` (scripts/parity.sh) covers the deterministic surface with a
docker shim. doctor/adopt against a **live** daemon are verified on a
parallel stand — never against the prod project.

## Rules

- Prod (`amnezia-mesh-awg` / `amnezia-mesh-dns` containers, prod compose
  project, `/opt/amnezia-mesh` or wherever config lives) is **never**
  stopped, restarted or reconfigured for tests.
- The stand is a separate compose project with a separate name, subnet,
  ports and state dir, so it can coexist with prod.
- `adopt --inspect` against a **copy** of the prod `awg0.conf` is safe
  (read-only). A real `adopt --config <copy>` into the stand is also safe:
  it never touches the old container (only `--container <name>` does).

## Stand setup (run on the server)

```sh
STAND=/opt/awg-mesh-test          # state dir, outside prod paths
mkdir -p "$STAND"
# deploy dir needs: awg-mesh binary, docker-compose.yml, scripts/
#   docker-compose.yml/scripts: from the amnezia-mesh repo
#   awg-mesh: cross-compiled linux binary from amnezia-mesh-go (goreleaser artifact)

cd "$STAND"
export AWG_MESH_ROOT="$STAND"
export AWG_MESH_NONINTERACTIVE=1

# compose project name isolation: the stack must not clash with prod
# containers/ports. Use a dedicated compose file or COMPOSE_PROJECT_NAME:
export COMPOSE_PROJECT_NAME=awg-mesh-test
# plus a test subnet and non-overlapping published port in init args below.
```

## Checks

```sh
# 1. init with test subnet + free host port (NOT 443/41480 if prod uses them)
./awg-mesh init --host <server-ip> --port 51443 --subnet 10.213.0.0/24 \
  --image amneziavpn/amneziawg-go:latest

# 2. doctor — all 7 checks green on the live stand
./awg-mesh doctor

# 3. peer lifecycle incl. real keygen + syncconf against the live container
./awg-mesh peer add stand-phone
./awg-mesh peer rename stand-phone stand-phone-2
./awg-mesh peer revoke stand-phone-2

# 4. read-only preview of a COPY of the prod conf (nothing changes)
scp prod:/opt/amnezia/awg/awg0.conf ./prod-awg0.conf   # example path
./awg-mesh adopt --inspect --config ./prod-awg0.conf \
  --image amneziavpn/amneziawg-go:latest --json

# 5. real adopt of the COPY into the stand (keys/PSKs carried over verbatim)
./awg-mesh adopt --config ./prod-awg0.conf \
  --image amneziavpn/amneziawg-go:latest --host <server-ip> --port 51443
```

## Parity against the Python CLI (same server)

Run the same steps with the Python CLI from the amnezia-mesh checkout
(`AWG_MESH_ROOT` pointed at a second stand dir) and diff `config/` trees;
normalize OBF values and timestamps as scripts/parity.sh does. Both CLIs
drive the same live containers sequentially — that is the point of the
stand: real `awg syncconf`, real dnsmasq, real handshakes.

## Cleanup

```sh
cd "$STAND" && COMPOSE_PROJECT_NAME=awg-mesh-test docker compose down
rm -rf "$STAND"
```

Prod remains untouched throughout; verify with
`docker ps` before and after — prod containers' uptime must be unchanged.
