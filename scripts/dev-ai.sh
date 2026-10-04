#!/usr/bin/env bash
# Starts the dev stack (make dev) with the AI provider enabled, so AI-authored
# js checks work locally (spec 2026-10-03-07, wiki/runbooks/ai-provider.md).
# The API key is read from gopass at start-up and never written to disk.
#
# Usage: scripts/dev-ai.sh [make-args...]     e.g. scripts/dev-ai.sh DEMO=false
# Override any of SP_AI_PROVIDER / SP_AI_BASE_URL / SP_AI_MODEL / SP_AI_API_KEY
# in the environment to use another provider.

set -euo pipefail

readonly GOPASS_KEY_PATH="solidping/byteplus/api-key"

usage() {
  sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

cd "$(dirname "$0")/.."

export SP_AI_PROVIDER="${SP_AI_PROVIDER:-openai}"
export SP_AI_BASE_URL="${SP_AI_BASE_URL:-https://ark.ap-southeast.bytepluses.com/api/v3}"
export SP_AI_MODEL="${SP_AI_MODEL:-glm-5-3-flash-260828}"

if [[ -z "${SP_AI_API_KEY:-}" ]]; then
  # With a cold gpg-agent cache gopass blocks on a pinentry dialog (pinentry-mac
  # opens a GUI window that can sit behind other apps), with nothing on the terminal.
  echo "Reading the API key from gopass (${GOPASS_KEY_PATH}); if this waits, unlock the pinentry dialog..." >&2
  if ! SP_AI_API_KEY="$(gopass show -o "${GOPASS_KEY_PATH}")" || [[ -z "${SP_AI_API_KEY}" ]]; then
    echo "error: set SP_AI_API_KEY or store the key in gopass at ${GOPASS_KEY_PATH}" >&2
    exit 1
  fi
  export SP_AI_API_KEY
fi

echo "AI mode: provider=${SP_AI_PROVIDER} model=${SP_AI_MODEL} base_url=${SP_AI_BASE_URL}"
exec make dev "$@"
