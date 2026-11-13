set shell := ["bash", "-euo", "pipefail", "-c"]
export PATH := justfile_directory() / ".tools/bin" + ":" + env_var("PATH")

default:
    @just --list

bootstrap:
    ./scripts/bootstrap

fmt:
    golangci-lint fmt

lint:
    ./scripts/lint

build:
    mkdir -p bin
    go build -trimpath -o bin/qual ./cmd/qual

test:
    go test -race -count=1 ./...

# Requires provisioned Linux namespace and pinned npm dependencies.
test-sandbox:
    ./tests/sandbox/run.sh

# CLI offline end-to-end entrypoint is supplied by application integration.
test-e2e:
    @test -x tests/e2e/run.sh || { echo 'ci_unavailable: CLI end-to-end integration is not installed' >&2; exit 1; }
    ./tests/e2e/run.sh

check:
    ./scripts/check

actionlint:
    actionlint -color

shellcheck:
    shellcheck scripts/bootstrap scripts/lint scripts/check

# Requires network access to the public Go vulnerability database.
vuln:
    govulncheck ./...

# Paid checks require explicit opt-in and the persistent budget gate.
test-llm:
    ./scripts/test-llm

# Compatibility alias; the same mandatory budget guard applies.
live: test-llm

# Requires npm advisory registry access; reports known upstream advisories honestly.
vuln-sandbox:
    npm audit --prefix sandbox
