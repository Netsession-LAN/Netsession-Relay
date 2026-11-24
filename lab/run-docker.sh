#!/bin/bash
# Run the network lab on a developer PC (Windows Git-Bash with Docker Desktop, or Linux with Docker).
# Builds the Linux binary and runs lab/ci.sh in a privileged container (own network namespaces,
# nothing touches the host network).
# LAB_COVER=1 builds the binary with coverage instrumentation; every process of the lab then writes its coverage
# into lab/.cover (go tool covdata, see tools/coverage.sh).
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p lab/.bin
cover=() env=()
if [ -n "${LAB_COVER:-}" ]; then
  rm -rf lab/.cover && mkdir -p lab/.cover
  cover=(-cover -covermode=atomic -coverpkg=./...)
  env=(-e GOCOVERDIR=/lab/.cover)
fi
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build "${cover[@]}" -o lab/.bin/nsrelay ./cmd/nsrelay
LABDIR=$(cd lab && (pwd -W 2>/dev/null || pwd))
MSYS_NO_PATHCONV=1 docker run --rm --privileged "${env[@]}" -v "$LABDIR:/lab" debian:trixie-slim bash /lab/ci.sh
