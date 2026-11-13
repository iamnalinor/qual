#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
go test -race -count=1 ./internal/cli -run TestExecutable
