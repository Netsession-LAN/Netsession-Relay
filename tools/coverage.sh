#!/bin/sh
# Test coverage of unit tests and the network lab together (Go's covdata format, Go >= 1.20).
# Usage (repo root, Linux or Git-Bash with Docker): sh tools/coverage.sh
# Writes coverage.out (text profile, for coverage services) and prints the total and the per-package numbers.
set -eu
cd "$(dirname "$0")/.."
rm -rf .cover-unit && mkdir -p .cover-unit
go test -count=1 -cover -covermode=atomic -coverpkg=./... ./... -args -test.gocoverdir="$PWD/.cover-unit" >/dev/null
LAB_COVER=1 bash lab/run-docker.sh >/dev/null
go tool covdata percent -i=.cover-unit,lab/.cover | sed 's#github.com/Netsession-LAN/Netsession-Relay/##' | sort
go tool covdata textfmt -i=.cover-unit,lab/.cover -o coverage.out
go tool cover -func=coverage.out | tail -1
