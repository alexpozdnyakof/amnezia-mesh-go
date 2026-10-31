.PHONY: test parity snapshot release

test:
	go test ./...

# Side-by-side parity harness against the Python reference CLI
# (AWG_MESH_PY=../amnezia-mesh by default). Docker is shimmed; nothing
# touches a real daemon, and never the prod server.
parity:
	bash scripts/parity.sh

# Local snapshot of the macOS cask (both darwin archs).
# Needs the sibling ../amnezia-mesh-tui checkout (or AWG_MESH_TUI).
snapshot:
	bash scripts/stage-mac-bundle.sh
	goreleaser release --snapshot --clean

# Tag-driven release. CI sets AWG_MESH_DARWIN_ARCHS to both darwin archs.
release:
	bash scripts/stage-mac-bundle.sh
	goreleaser release --clean
