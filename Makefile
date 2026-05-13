.PHONY: all fmt vet run-tests open_coverage clean e2e _all

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

# Default target. Runs gofmt, go vet, race tests + 100%
# coverage gate, and the e2e smoke — all in parallel via a recursive
# `make -j`. Wall-time is roughly the slowest single task, not the sum.
#
# This is exactly what CI runs — no separate "fast" mode. If you want
# to iterate faster locally, run `go test ./...` directly.
all:
	@$(MAKE) -j --no-print-directory _all
	@echo "✓ all green"

# Internal aggregate target — every prereq is independent and self-
# contained, so `make -j` can fan them out.
_all: fmt vet run-tests e2e

fmt:
	$(call RUN,gofmt,gofmt -w .)

vet:
	$(call RUN,go vet clean,go vet ./...)

# Run unit tests with race + shuffle + fresh cache + 100% coverage gate.
# Standalone — `make all` runs this in parallel with fmt/vet/e2e.
run-tests:
	@go clean -testcache
	$(call RUN,tests pass,go test -race -shuffle=on -cover ./... -coverprofile=coverage.tmp.out)
	$(call RUN,coverage clean,go run github.com/kfet/covgate/cmd/covgate@v0.1.0 -profile=coverage.tmp.out -out=coverage.out -ignore=.covignore -min=100)
	@rm -f coverage.tmp.out

open_coverage:
	go tool cover -html=coverage.out

clean:
	rm -f coverage.out coverage.tmp.out

# End-to-end smoke: builds the binary, exercises ClientLogin → subscription
# list → stream/contents → edit-tag → unread-count, plus a UI login + home
# fetch. Run as part of `make all`; also invocable standalone.
e2e:
	$(call RUN,e2e smoke,E2E=1 go test -count=1 -timeout=60s ./e2e/...)
