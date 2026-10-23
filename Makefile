.PHONY: test parity

test:
	go test ./...

# Stage 4: side-by-side parity harness against the Python CLI (see PLAN.md).
parity:
	@echo "parity harness lands in stage 4 (see PLAN.md)"
