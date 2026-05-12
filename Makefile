.PHONY: all check fmt fmtcheck vet staticcheck _staticcheck run-tests open_coverage clean

# Quiet runner: $(call RUN,label,cmd) — runs cmd silently, prints "✓ label" on
# success, dumps captured output and exits non-zero on failure. Set V=1 for
# verbose output.
ifdef V
  define RUN
	@echo "→ $(1)"
	@$(2)
  endef
else
  define RUN
	@_log=$$(mktemp); \
	if ( $(2) ) > $$_log 2>&1; then \
		echo "✓ $(1)"; rm -f $$_log; \
	else \
		rc=$$?; cat $$_log; rm -f $$_log; exit $$rc; \
	fi
  endef
endif

# Default (and only) target. gofmt + go vet + staticcheck + unit tests with
# the race detector, shuffled order, fresh cache, and a 100% coverage gate.
# This is also exactly what CI runs — no separate "fast" mode. If you want
# to iterate faster locally, run `go test ./...` directly.
all: run-tests
	@echo "✓ all green"

# Static gates (gofmt + go vet + staticcheck if installed).
check: fmtcheck vet staticcheck

fmt:
	@gofmt -w .

fmtcheck:
	$(call RUN,gofmt clean,out=$$(gofmt -l .); test -z "$$out" || { echo "gofmt offenders (run 'make fmt'):"; echo "$$out"; exit 1; })

vet:
	$(call RUN,go vet clean,go vet ./...)

# staticcheck is optional. Install with:
#   go install honnef.co/go/tools/cmd/staticcheck@latest
staticcheck:
	@if ! command -v staticcheck >/dev/null 2>&1; then \
		echo "(staticcheck not installed — skipping)"; exit 0; \
	fi; \
	$(MAKE) --no-print-directory _staticcheck

_staticcheck:
	$(call RUN,staticcheck clean,out=$$(staticcheck ./... 2>&1 | grep -v 'file requires newer Go version' || true); test -z "$$out" || { echo "$$out"; exit 1; })

# Run unit tests with race + shuffle + fresh cache + 100% coverage gate.
run-tests: check
	@go clean -testcache
	$(call RUN,tests pass,go test -race -shuffle=on -cover ./... -coverprofile=coverage.tmp.out)
	$(call RUN,coverage clean,go run github.com/kfet/covgate/cmd/covgate@v0.1.0 -profile=coverage.tmp.out -out=coverage.out -ignore=.covignore -min=100)
	@rm -f coverage.tmp.out

open_coverage:
	go tool cover -html=coverage.out

clean:
	rm -f coverage.out coverage.tmp.out
