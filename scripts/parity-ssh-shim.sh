#!/bin/bash
# ssh/scp shim for the parity harness remote-mode steps (stage 7, thin ssh
# proxy). Real ssh/scp are never invoked. Copied into the shim dir as both
# `ssh` and `scp`; dispatches on basename.
#
# ssh: logs "ssh <alias> <remote command>", answers each remote command
# with canned output (deterministic, side-effect free):
#   *peer add*   -> human line, exit 0 (a mutation: CLI then runs scp)
#   *revoke*     -> nothing, exit 1 (remote failure: no sync, rc passes through)
#   *peer list*  -> canned JSON
#   *doctor*     -> canned JSON
#   anything else-> echoes the remote command
# scp: logs "scp <args>" and mirrors $PARITY_CANNED_PEERS into <dest>/peers.
log="${SHIM_LOG:-/dev/null}"
printf '%s %s\n' "$(basename "$0")" "$*" >>"$log" 2>/dev/null || true

if [ "$(basename "$0")" = "scp" ]; then
  dest="${!#}"
  mkdir -p "$dest"
  cp -R "${PARITY_CANNED_PEERS:?}" "$dest/peers"
  exit 0
fi

cmd="${2:-}"
case "$cmd" in
  *"peer add"*)  printf 'remote peer added\n' ;;
  *"revoke"*)    exit 1 ;;
  *"peer list"*) printf '{"ok": true, "peers": []}\n' ;;
  *"doctor"*)    printf '{"ok": true, "checks": [{"name":"stub","ok":true,"detail":"remote"}]}\n' ;;
  *)             printf 'ssh-stub: %s\n' "$cmd" ;;
esac
exit 0
