# graph-harness — task runner (casey/just)
# Usage: `just <recipe>`. `just --list` to see all recipes.

# --- Build metadata (overridable via `just VERSION=0.1.0 build`) -------------
VERSION := env("VERSION", "0.1.0-dev")
COMMIT := `git rev-parse --short HEAD 2>/dev/null || echo "none"`
DATE := `date -u +%Y-%m-%dT%H:%M:%SZ`
LDFLAGS := "-X github.com/shivamstaq/graph-harness/internal/version.Version=" + VERSION + " -X github.com/shivamstaq/graph-harness/internal/version.Commit=" + COMMIT + " -X github.com/shivamstaq/graph-harness/internal/version.Date=" + DATE

# gotit binary version pinned to whatever go.mod uses for the library, so the
# TUI and the runner stay in lockstep.
GOTIT_VERSION := `go list -m github.com/shivamstaq/gotit 2>/dev/null | awk '{v=$2} END{print (v ? v : "latest")}'`

# Default recipe: list available recipes.
default:
    @just --list

# Compile the CLI binary into ./bin/.
build:
    mkdir -p bin
    go build -ldflags '{{ LDFLAGS }}' -o bin/graph-harness ./cmd/graph-harness

# Install graph-harness into $GOPATH/bin (or $GOBIN).
install:
    go install -ldflags '{{ LDFLAGS }}' ./cmd/graph-harness

# Run the unit-test suite with the race detector and no test caching.
test:
    go test -race -count=1 ./...

# Lint via golangci-lint v2 (pinned in go.mod tool directive).
lint:
    go tool golangci-lint run

# Format the tree with gofumpt (stricter superset of gofmt).
fmt:
    go tool gofumpt -l -w .

# Check formatting without rewriting (used in CI).
fmt-check:
    @diff=$(go tool gofumpt -l .); \
        if [ -n "$diff" ]; then echo "Files need formatting:"; echo "$diff"; exit 1; fi

# Tidy go.mod / go.sum and verify checksums.
tidy:
    go mod tidy
    go mod verify

# Smoke test — exercises the end-to-end demos from plan §4 (P0 single-Go +
# P1 polyglot). Both run when present; failure of either fails the recipe.
smoke:
    @if [ -x tests/smoke/p0/run.sh ]; then \
        bash tests/smoke/p0/run.sh; \
    else \
        echo "P0 smoke pending P0.T49"; \
    fi
    @if [ -x tests/smoke/p1/demo.sh ]; then \
        bash tests/smoke/p1/demo.sh; \
    else \
        echo "P1 smoke pending P1.T41"; \
    fi

# E2E suite — gotit-driven YAML specs under tests/e2e/specs/. CI surface.
e2e:
    go test -count=1 ./tests/e2e/...

# Run a single E2E wave (e.g. `just e2e-wave boilerplate`).
e2e-wave WAVE:
    go test -count=1 ./tests/e2e/... -run TestE2E/{{ WAVE }}

# Ensure the `gotit` binary is installed at the version go.mod pins. Idempotent.
gotit-install:
    @if ! command -v gotit >/dev/null 2>&1 || \
        [ "$(gotit version 2>/dev/null | awk '{print $NF}')" != "{{ GOTIT_VERSION }}" ]; then \
        echo "installing github.com/shivamstaq/gotit/cmd/gotit@{{ GOTIT_VERSION }}"; \
        go install github.com/shivamstaq/gotit/cmd/gotit@{{ GOTIT_VERSION }}; \
    fi

# Interactive E2E TUI (requires a TTY; falls back to a clear error otherwise).
gotit: gotit-install
    gotit

# Headless E2E via the gotit binary, optionally filtered: `just gotit-run boilerplate/cli`.
gotit-run PATTERN="": gotit-install
    gotit run {{ PATTERN }}

# Environment-readiness report from gotit's own doctor.
gotit-doctor: gotit-install
    gotit doctor

# Vet the tree. structtag is disabled because Participle uses non-standard
# struct tag syntax intentionally (the same is configured in .golangci.yml).
vet:
    go vet -structtag=false ./...

# Full CI pipeline (matches .github/workflows/ci.yml).
ci: tidy vet fmt-check lint test e2e smoke

# Remove build artifacts and local SQLite event-log files.
clean:
    rm -rf bin dist
    find . -name '*.db' -o -name '*.db-wal' -o -name '*.db-shm' | xargs -r rm -f
