# herdr-virtualboard — development gates.
#
# `make gates` is the single command to run before pushing; CI runs exactly the
# same target, so a green local run means a green pipeline.

GO       ?= go
BIN      := bin/hvb
VERSION  := $(shell cat VERSION 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
TEMPLATE_SOURCE ?= ../template-base

.PHONY: all build install clean fmt fmt-check vet test test-race cover e2e gates integration sync-skill tidy help

all: build

## build: compile bin/hvb
build:
	@./scripts/build.sh

## install: build and copy hvb onto your PATH
install: build
	@./scripts/install-cli.sh

## fmt: format every Go file
fmt:
	@gofmt -w $(shell find . -name '*.go' -not -path './vendor/*')

## fmt-check: fail when anything is unformatted
fmt-check:
	@unformatted=$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*')); \
	if [ -n "$$unformatted" ]; then \
		echo "these files need gofmt:"; echo "$$unformatted"; exit 1; \
	fi

## vet: run go vet
vet:
	@$(GO) vet ./...

## test: run the unit tests
test:
	@$(GO) test ./...

## test-race: run the unit tests under the race detector
test-race:
	@$(GO) test -race ./...

## cover: report test coverage per package
cover:
	@$(GO) test -coverprofile=coverage.out ./... >/dev/null
	@$(GO) tool cover -func=coverage.out | tail -25

## e2e: run the end-to-end suite against stub vb and herdr binaries
e2e: build
	@./e2e/run-all.sh

## integration: real vb + hvb + sibling template contracts and report catalog
integration: build
	@test -n "$(VB_REAL_BIN)" || { echo "Set VB_REAL_BIN to the real vb binary under test" >&2; exit 1; }
	@TEMPLATE_SOURCE="$(TEMPLATE_SOURCE)" VB_REAL_BIN="$(VB_REAL_BIN)" HVB_BIN="$(CURDIR)/$(BIN)" bash integration/contracts.sh
	@TEMPLATE_SOURCE="$(TEMPLATE_SOURCE)" VB_REAL_BIN="$(VB_REAL_BIN)" bash integration/reports.sh

## sync-skill: copy the published contract into the embedded one
sync-skill:
	@cp skill/SKILL.md internal/dispatch/skill.md
	@echo "synced skill/SKILL.md -> internal/dispatch/skill.md"

## tidy: tidy go.mod and go.sum
tidy:
	@$(GO) mod tidy

## gates: everything CI runs
gates: fmt-check vet test-race e2e
	@echo "all gates passed"

## clean: remove build output
clean:
	@rm -rf bin coverage.out

## help: list these targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
