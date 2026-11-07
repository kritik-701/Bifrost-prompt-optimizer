#!/usr/bin/env bash
# Sends one deliberately bloated prompt through a running Bifrost gateway and
# prints the plugin's stats. The plugin runs BEFORE the provider call, so the
# numbers update even if the provider rejects a dummy API key.
#
# Usage:
#   scripts/smoke.sh
#   BIFROST_URL=http://localhost:8080 STATS_URL=http://127.0.0.1:9091 MODEL=openai/gpt-4o-mini scripts/smoke.sh
set -euo pipefail

BIFROST_URL="${BIFROST_URL:-http://localhost:8080}"
STATS_URL="${STATS_URL:-http://127.0.0.1:9091}"
MODEL="${MODEL:-openai/gpt-4o-mini}"

# The e-mail address and the key below are fake values made up for this test.
read -r -d '' PAYLOAD <<JSON || true
{
  "model": "${MODEL}",
  "messages": [
    {
      "role": "user",
      "content": "Hello    Bifrost.   \n\n\n\n\nIn order to   test this,   please   summarise   the   text below.   \n\n\n\nContact: jane.doe@example.com   key: sk-test-abcdefghijklmnopqrstuvwxyz0123456789   \n\n\n\n"
    }
  ]
}
JSON

echo "==> Checking the dashboard is up at ${STATS_URL}"
curl -fsS "${STATS_URL}/healthz" >/dev/null || {
  echo "Cannot reach ${STATS_URL}. Is Bifrost running with the plugin loaded?" >&2
  exit 1
}

echo "==> Sending a bloated prompt to ${BIFROST_URL}"
curl -sS -X POST "${BIFROST_URL}/v1/chat/completions" \
  -H 'Content-Type: application/json' \
  -d "${PAYLOAD}" || true
echo

echo "==> Plugin stats"
curl -fsS "${STATS_URL}/api/stats"
echo
echo "Open ${STATS_URL} in your browser for the live dashboard."
