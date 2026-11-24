#!/bin/bash
# Run the network lab on a developer PC (Windows Git-Bash with Docker Desktop, or Linux with Docker).
# Builds the Linux binary and runs lab/ci.sh in a privileged container (own network namespaces,
# nothing touches the host network).
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p lab/.bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o lab/.bin/nsrelay ./cmd/nsrelay
LABDIR=$(cd lab && (pwd -W 2>/dev/null || pwd))
MSYS_NO_PATHCONV=1 docker run --rm --privileged -v "$LABDIR:/lab" debian:trixie-slim bash /lab/ci.sh
