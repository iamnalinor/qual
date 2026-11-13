#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
go test -race -tags sandbox -count=1 ./internal/cli -run TestSandboxResponsesReview
