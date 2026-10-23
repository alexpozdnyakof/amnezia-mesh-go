.PHONY: test parity snapshot release

test:
	go test ./...

# Side-by-side parity harness against the Python reference CLI
# (AWG_MESH_PY=../amnezia-mesh by default). Docker is shimmed; nothing
# touches a real daemon, and never the prod server.
parity:
	bash scripts/parity.sh

# Local, no-publish build of all release artifacts (needs goreleaser).
snapshot:
	goreleaser release --snapshot --clean

# Tag-driven release: cross-compiled binaries + GitHub Release + brew formula.
release:
	goreleaser release --clean
