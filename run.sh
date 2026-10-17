#!/usr/bin/env bash
set -euo pipefail

if [[ -f .env ]]; then
  set -a
  # .env is a trusted, local shell-style file and is ignored by Git.
  source .env
  set +a
fi

exec go run ./cmd/server
