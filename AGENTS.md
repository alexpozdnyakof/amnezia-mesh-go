# amnezia-mesh-go — orientation for agents

Go port of the `amnezia-mesh` CLI (sibling repo `../amnezia-mesh`, frozen
Python reference). Single static binary (`cmd/awg-mesh`), stdlib +
`golang.org/x/sys` only. **Parity with the Python CLI is the contract:**
byte-identical artifacts, stdout/stderr and exit codes, enforced by
`scripts/parity.sh`.

## Commands

```sh
go test ./...          # unit + golden tests (fixtures are the contract)
make parity            # 12-step side-by-side harness vs the Python CLI
                       # (docker shimmed; no daemon, never the prod server)
make snapshot          # local goreleaser build (binaries + brew cask)
```

## Layout

`internal/{envfile,peers,render,ojson,profile,keygen,compose,doctor,adopt,mesh,cli,skill}`,
parity harness in `scripts/`, live-stand procedure in `docs/parity-live.md`,
operator guide embedded via `internal/skill` (`awg-mesh skill [--install]`).

## Hard rules

- **Never touch the production server** (containers `amnezia-mesh-awg` /
  `amnezia-mesh-dns` or its `config/`) from this repo. Live checks go to the
  parallel stand (`docs/parity-live.md`).
- Any behavior change must land in BOTH repos and keep `make parity` at
  zero diffs; new artifacts join the harness's compared tree.
- Golden fixtures (`testdata/fixtures/golden/`) are byte-exact copies of the
  Python repo's `tests/fixtures/golden/` — copy, don't regenerate by hand.
