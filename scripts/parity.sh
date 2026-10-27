#!/bin/bash
# Side-by-side parity harness: Python reference CLI vs Go port.
#
# Runs an identical command matrix against both implementations on
# throwaway roots (AWG_MESH_ROOT), with a docker shim on PATH so init /
# adopt / peer add / doctor execute deterministically and never touch a
# real daemon — and never the prod server. Compared per step, after
# normalization, byte-for-byte:
#   - exit codes
#   - stdout / stderr (root paths, timestamps, OBF values normalized)
#   - config trees (server.env, peers.db, awg0.conf, dnsmasq.conf,
#     peers.hosts, start.sh, peers/*.conf; *.vpn excluded — deflate streams
#     legitimately differ between zlib implementations, profile semantic
#     parity is covered by unit tests)
#
# Normalized: OBF_* values (Python draws real randomness, Go uses
# AWG_MESH_SEED), ISO-8601 timestamps, per-root paths.
#
# Usage: make parity   (or: AWG_MESH_PY=../amnezia-mesh scripts/parity.sh)
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
PY_REPO="${AWG_MESH_PY:-$(cd "$REPO/.." && pwd)/amnezia-mesh}"
PY="$PY_REPO/.venv/bin/python"
[ -x "$PY" ] || PY="python3"

WORK="$(mktemp -d -t awg-mesh-parity)"
trap 'rc=$?; if [ "$rc" -eq 0 ]; then rm -rf "$WORK"; else echo "work preserved: $WORK" >&2; fi' EXIT

GOBIN="$WORK/awg-mesh"
if ! (cd "$REPO" && go build -o "$GOBIN" ./cmd/awg-mesh); then
  echo "go build failed" >&2
  exit 1
fi

SHIMDIR="$WORK/shim"
mkdir -p "$SHIMDIR"
cp "$HERE/parity-docker-shim.sh" "$SHIMDIR/docker"
chmod +x "$SHIMDIR/docker"
export SHIM_LOG="$WORK/shim.log"

export AWG_MESH_SEED=7 # Go-side deterministic obfuscation (Python ignores it)

PYROOT="$WORK/py-root"
GROOT="$WORK/go-root"
PYADOPT="$WORK/py-adopt"
GOADOPT="$WORK/go-adopt"
mkdir -p "$PYROOT" "$GROOT" "$PYADOPT" "$GOADOPT"

fails=0
step_no=0
PY_CUR=""
GO_CUR=""

# normalize a text stream: root paths, timestamps, obf env lines
norm_stream() { # $1 = root path to blank out
  sed -E \
    -e "s|$1|<ROOT>|g" \
    -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z/<TS>/g' \
    -e 's/^(OBF_[A-Z0-9]+)=.*/\1=<NORM>/'
}

norm_file() { # $1 = file, in place
  sed -i.paritybak -E \
    -e 's/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z/<TS>/g' \
    -e 's/^(OBF_[A-Z0-9]+)=.*/\1=<NORM>/' \
    "$1" 2>/dev/null || true
  rm -f "$1.paritybak"
}

norm_conf_obf() { # $1 = file — normalize obf "Key = value" lines (awg0.conf, .conf)
  sed -i.paritybak -E \
    -e 's/^Jc = .*/Jc = <NORM>/' \
    -e 's/^Jmin = .*/Jmin = <NORM>/' \
    -e 's/^Jmax = .*/Jmax = <NORM>/' \
    -e 's/^S[1-4] = .*/S = <NORM>/' \
    -e 's/^H[1-4] = .*/H = <NORM>/' \
    -e 's/^HeaderProtectionKey = .*/HeaderProtectionKey = <NORM>/' \
    "$1" 2>/dev/null || true
  rm -f "$1.paritybak"
}

norm_tree() { # $1 = root dir
  find "$1" -type f ! -name '*.vpn' | while IFS= read -r f; do
    norm_file "$f"
    case "$f" in
      *.conf) norm_conf_obf "$f" ;;
    esac
  done
}

run_side() { # $1 = side(py|go) $2 = root; rest = args
  local side="$1" root="$2"
  shift 2
  if [ "$side" = py ]; then
    # cwd = the python repo so `python -m awg_mesh` resolves; the temp root
    # is injected via AWG_MESH_ROOT (root() env override).
    (cd "$PY_REPO" && PATH="$SHIMDIR:$PATH" AWG_MESH_ROOT="$root" \
      AWG_MESH_NONINTERACTIVE=1 "$PY" -m awg_mesh "$@" \
      >"$WORK/$side.out" 2>"$WORK/$side.err")
  else
    (cd "$root" && PATH="$SHIMDIR:$PATH" AWG_MESH_ROOT="$root" \
      AWG_MESH_NONINTERACTIVE=1 "$GOBIN" "$@" \
      >"$WORK/$side.out" 2>"$WORK/$side.err")
  fi
  echo $? >"$WORK/$side.code"
}

check_code() { # $1 = step label
  if [ "$(cat "$WORK/py.code")" != "$(cat "$WORK/go.code")" ]; then
    echo "FAIL  $1: exit code python=$(cat "$WORK/py.code") go=$(cat "$WORK/go.code")"
    fails=$((fails + 1))
    return 1
  fi
  return 0
}

check_stream() { # $1 = which(out|err) $2 = step label
  norm_stream "$PY_CUR" <"$WORK/py.$1" >"$WORK/py.$1.norm"
  norm_stream "$GO_CUR" <"$WORK/go.$1" >"$WORK/go.$1.norm"
  if ! diff -u "$WORK/py.$1.norm" "$WORK/go.$1.norm" >"$WORK/$1.diff" 2>&1; then
    echo "FAIL  $2: $1 differs"
    cat "$WORK/$1.diff"
    fails=$((fails + 1))
    return 1
  fi
  return 0
}

check_trees() { # $1 = step label
  norm_tree "$PY_CUR"
  norm_tree "$GO_CUR"
  if ! diff -r -x '*.vpn' "$PY_CUR" "$GO_CUR" >"$WORK/tree.diff" 2>&1; then
    echo "FAIL  $1: config trees differ"
    cat "$WORK/tree.diff"
    fails=$((fails + 1))
    return 1
  fi
  return 0
}

step() { # $1 = py root, $2 = go root, $3 = label, rest = args
  local before="$fails"
  PY_CUR="$1"; GO_CUR="$2"; local label="$3"
  shift 3
  step_no=$((step_no + 1))
  run_side py "$PY_CUR" "$@"
  run_side go "$GO_CUR" "$@"
  check_code "$label" || true
  check_stream out "$label" || true
  check_stream err "$label" || true
  check_trees "$label" || true
  if [ "$fails" -eq "$before" ]; then
    printf 'ok    %2d  %s\n' "$step_no" "$label"
  else
    printf 'FAIL  %2d  %s (diffs so far=%d)\n' "$step_no" "$label" "$fails"
  fi
}

echo "parity harness: python=$PY_REPO go=$REPO"
echo "------------------------------------------------------------"

# --- flow 1: init + lifecycle ------------------------------------------------
step "$PYROOT" "$GROOT" "init" \
  init --host 198.51.100.10 --image shim-image --port 443
step "$PYROOT" "$GROOT" "doctor --json"    doctor --json
step "$PYROOT" "$GROOT" "peer list"        peer list
step "$PYROOT" "$GROOT" "peer list --json" peer list --json
step "$PYROOT" "$GROOT" "peer add"         peer add parity-dev1
step "$PYROOT" "$GROOT" "peer rename"      peer rename parity-dev1 parity-dev2
step "$PYROOT" "$GROOT" "peer revoke"      peer revoke parity-dev2

# --- flow 2: adopt -----------------------------------------------------------
step "$PYADOPT" "$GOADOPT" "adopt --inspect --json" \
  adopt --inspect --config "$REPO/testdata/fixtures/golden/awg0.conf" \
  --image shim-image
step "$PYADOPT" "$GOADOPT" "adopt --inspect (human)" \
  adopt --inspect --config "$REPO/testdata/fixtures/golden/awg0.conf" \
  --image shim-image
step "$PYADOPT" "$GOADOPT" "adopt --config" \
  adopt --config "$REPO/testdata/fixtures/golden/awg0.conf" \
  --image shim-image --host 198.51.100.20 --port 443 \
  --dns2 1.0.0.1 --rdp-reject
step "$PYADOPT" "$GOADOPT" "adopt: doctor" doctor --json
step "$PYADOPT" "$GOADOPT" "adopt: list"   peer list --json

echo "------------------------------------------------------------"
if [ "$fails" -eq 0 ]; then
  echo "PARITY OK: $step_no steps, zero diffs"
  exit 0
else
  echo "PARITY FAILED: $fails diff(s) across $step_no steps (shim log: $WORK/shim.log)"
  exit 1
fi
