#!/bin/bash
# Docker shim for the amnezia-mesh-go parity harness (scripts/parity.sh).
#
# Deterministic and side-effect free: answers every docker call the Python
# and Go CLIs make during init/adopt/peer/doctor, with FIXED key material so
# both implementations produce byte-identical state. Real docker is never
# invoked; there is nothing to clean up and no prod to break.
log="${SHIM_LOG:-/dev/null}"
printf '%s\n' "$*" >>"$log" 2>/dev/null || true

# Key generation: fixed outputs, so python and go write identical keys.
# (Standalone tokens only — the I1-I5 probe script is one quoted argument
# containing "awg genkey" as a substring and never matches here.)
for a in "$@"; do
  case "$a" in
    genkey) echo "SHIMpriv0000000000000000000000000000000000A"; exit 0 ;;
    genpsk) echo "SHIMpsk00000000000000000000000000000000000C"; exit 0 ;;
    pubkey) echo "SHIMpub00000000000000000000000000000000000B"; exit 0 ;;
  esac
done

sub="${1:-}"; arg2="${2:-}"
case "$sub" in
  pull|info|stop|start|rm)
    exit 0 ;;
  port)
    echo "41480/udp -> 0.0.0.0:443"
    exit 0 ;;
  inspect)
    echo "shim-image:latest"
    exit 0 ;;
  exec)
    exit 0 ;;
  run)
    # I1-I5 probe: `run ... --entrypoint sh <img> -c <script>` -> SUPPORTED
    case "$*" in
      *"mktemp -d"*) echo "SUPPORTED" ;;
    esac
    exit 0 ;;
  compose)
    case "$arg2" in
      up|down|restart)
        exit 0 ;;
      ps)
        case "$*" in
          *"{{.Name}} {{.Status}}"*)
            printf 'amnezia-mesh-awg Up 2 minutes\namnezia-mesh-dns Up 2 minutes\n' ;;
          *)
            printf 'Up 2 minutes\n' ;;
        esac
        exit 0 ;;
      exec)
        case "$*" in
          *"awg show"*)
            printf 'interface: awg0\n  listening port: 41480\n  private key: (hidden)\n' ;;
          *"ip_forward"*)
            echo 1 ;;
          *nslookup*)
            echo "Name:	hub.awg"
            echo "Address: 100.64.0.1" ;;
          *)
            # awg syncconf <iface> /dev/stdin — drain stdin, succeed
            cat >/dev/null 2>&1 || true ;;
        esac
        exit 0 ;;
    esac
    exit 0 ;;
esac
exit 0
