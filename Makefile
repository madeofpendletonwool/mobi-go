# Development entry points. CI runs the same commands (ci.yml).

GO ?= go
KINDLEUNPACK_PATH ?=

# The full local gate: everything CI checks, in one command.
check:
	gofmt -l .
	$(GO) vet ./...
	$(GO) install honnef.co/go/tools/cmd/staticcheck@latest
	staticcheck ./...
	$(GO) test -race ./...

test:
	$(GO) test -race ./...

# Regenerate the synthetic fixture corpus (byte-deterministic) and the
# golden oracle digests. Offline, on demand: needs a KindleUnpack
# checkout (or `pip install mobi`); CI never runs this — it only runs
# the Go comparisons against the committed digests.
regen-golden:
	$(GO) run ./tools/fixgen
	KINDLEUNPACK_PATH=$(KINDLEUNPACK_PATH) python3 tools/oracle-digest.py

# Short local fuzz pass over every target (~10s each). CI runs the
# longer 30s-per-target pass on push.
fuzz-short:
	for target in FuzzOpen FuzzPDB FuzzHeaders FuzzPalmDOC FuzzHuffCDIC \
	              FuzzResource FuzzINDX FuzzKF8Assembly FuzzTextAssembly; do \
		$(GO) test -run='^$$' -fuzz=$$target -fuzztime=10s . || exit 1; \
	done

.PHONY: check test regen-golden fuzz-short
