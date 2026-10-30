# strata development tasks. `make check` runs everything CI runs.

GO        ?= go
BIN       ?= bin/strata
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG       := github.com/rossedman/strata/internal/cli
LDFLAGS   := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)
COVERMIN  ?= 90
FUZZTIME  ?= 30s
BENCHOUT  ?= bench.txt
BENCH     ?= .
BASE      ?= main
CRAWLBENCH := Crawl|Recrawl|Refresh|Write
BENCHPKGS := ./internal/crawl ./internal/service ./internal/store
DOCS      := uv run --no-project --with-requirements requirements-docs.txt mkdocs
VACUUM    ?= vacuum

.PHONY: build test cover bench bench-compare fuzz lint fmt generate generate-check vuln docs docs-build docs-serve release-check check clean

## build: compile the strata binary (needs cgo for DuckDB)
build:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) .

## test: run every test with the race detector
test:
	$(GO) test -race -count=1 ./...

## cover: measure coverage on hand-written code and fail below $(COVERMIN)%
cover:
	$(GO) test -count=1 -coverpkg=./... -coverprofile=coverage.out ./...
	$(GO) run ./tools/generated ./... > coverage.generated
	{ head -1 coverage.out; grep -v '^mode:' coverage.out | grep -vFf coverage.generated | grep -v '/examples/' | grep -v '/internal/testreg/' | grep -v '/internal/fixture/'; } > coverage.hand.out
	@$(GO) tool cover -func=coverage.hand.out | tail -1
	@$(GO) tool cover -func=coverage.hand.out | awk '/^total:/ {sub("%","",$$3); if ($$3+0 < $(COVERMIN)) {print "coverage " $$3 "% < $(COVERMIN)%"; exit 1}}'

## bench: run benchmarks matching BENCH and save results for benchstat
bench:
	$(GO) test -run='^$$' -bench='$(BENCH)' -benchmem -count=6 ./... | tee $(BENCHOUT)

## bench-compare: benchmark the crawl and refresh against BASE (default main), as CI does
bench-compare:
	rm -rf .bench-base && git worktree add .bench-base $(BASE)
	cd .bench-base && $(GO) test -run='^$$' -bench='$(CRAWLBENCH)' -benchmem -benchtime=3x -count=6 $(BENCHPKGS) > ../bench-base.txt || true
	git worktree remove --force .bench-base
	$(GO) test -run='^$$' -bench='$(CRAWLBENCH)' -benchmem -benchtime=3x -count=6 $(BENCHPKGS) > bench-pr.txt
	$(GO) tool benchstat base=bench-base.txt pr=bench-pr.txt

## fuzz: run every fuzz target for $(FUZZTIME)
fuzz:
	@for pkg in $$($(GO) list ./...); do \
	  dir=$$($(GO) list -f '{{.Dir}}' $$pkg); \
	  for t in $$(grep -ho '^func Fuzz[A-Za-z0-9_]*' $$dir/*_test.go 2>/dev/null | sed 's/func //'); do \
	    echo "fuzz $$pkg $$t"; \
	    $(GO) test -run='^$$' -fuzz="^$$t$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
	  done; \
	done

## lint: golangci-lint, OpenAPI lint, one-command-per-file and doc comments
lint:
	golangci-lint run ./...
	$(VACUUM) lint -d -r .vacuum.yaml api/openapi.yaml
	./tools/check-cmds.sh
	$(GO) run ./tools/doccheck ./...

## fmt: format Go code
fmt:
	gofmt -w $$(git ls-files '*.go')

## generate: regenerate API code from api/openapi.yaml
generate:
	$(GO) generate ./...

## generate-check: fail if generated code is stale
generate-check: generate
	git diff --exit-code -- internal/api pkg/client

## vuln: scan dependencies for known vulnerabilities
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

## docs: regenerate the command reference and copy the API spec into the site
docs:
	$(GO) run ./tools/gendocs
	cp api/openapi.yaml docs/reference/openapi.yaml

## docs-build: build the docs site strictly
docs-build: docs
	$(DOCS) build --strict

## docs-serve: serve the docs site locally
docs-serve: docs
	$(DOCS) serve

## release-check: validate the GoReleaser config
release-check:
	goreleaser check

## check: everything CI runs
check: generate-check build lint test cover vuln docs-build release-check
	$(GO) test -run='^$$' -bench=. -benchtime=1x ./...
	$(MAKE) fuzz FUZZTIME=10s

## clean: remove build and coverage output
clean:
	rm -rf bin dist site coverage.out coverage.hand.out coverage.generated $(BENCHOUT)
