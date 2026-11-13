#!/usr/bin/env bash
# Build the linux/amd64 croc binary that Ansible ships to the relay VM.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/croc-linux-amd64 .
echo "built dist/croc-linux-amd64"
