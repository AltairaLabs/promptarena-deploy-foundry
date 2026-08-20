#!/usr/bin/env bash
#
# Invoke a deployed Foundry hosted agent over the invocations protocol.
#
#   ./scripts/invoke.sh "What is the capital of France?"
#   ./scripts/invoke.sh --stream "List three colours"
#   ./scripts/invoke.sh --session <id> "and their hex codes?"
#
# Configure the target with environment variables:
#   FOUNDRY_ACCOUNT   Foundry account name        (required)
#   FOUNDRY_PROJECT   project name                (required)
#   FOUNDRY_AGENT     agent name                  (default: the pack id)
#
# Authentication uses your current `az login`. The endpoint needs the agents
# data actions; no built-in role grants them, so see docs/ for the custom role.
set -euo pipefail

ACCOUNT="${FOUNDRY_ACCOUNT:-}"
PROJECT="${FOUNDRY_PROJECT:-}"
AGENT="${FOUNDRY_AGENT:-}"
STREAM=false
SESSION=""

usage() {
    sed -n '2,16p' "$0" | sed 's|^# \{0,1\}||'
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --stream)  STREAM=true; shift ;;
        --session) SESSION="$2"; shift 2 ;;
        --agent)   AGENT="$2"; shift 2 ;;
        -h|--help) usage 0 ;;
        --) shift; break ;;
        -*) echo "unknown flag: $1" >&2; usage 1 ;;
        *) break ;;
    esac
done

MESSAGE="${1:-}"
if [ -z "$MESSAGE" ]; then
    echo "error: no message given" >&2
    usage 1
fi
for var in ACCOUNT PROJECT AGENT; do
    if [ -z "${!var}" ]; then
        echo "error: FOUNDRY_${var} is not set" >&2
        usage 1
    fi
done

TOKEN=$(az account get-access-token --scope "https://ai.azure.com/.default" \
    --query accessToken -o tsv)

URL="https://${ACCOUNT}.services.ai.azure.com/api/projects/${PROJECT}"
URL="${URL}/agents/${AGENT}/endpoint/protocols/invocations?api-version=v1"
# The invocations endpoint reads the session id from the query string only;
# a body field or header is forwarded to the container but does not bind it.
if [ -n "$SESSION" ]; then
    URL="${URL}&agent_session_id=${SESSION}"
fi

BODY=$(MESSAGE="$MESSAGE" STREAM="$STREAM" python3 -c '
import json, os
print(json.dumps({
    "message": os.environ["MESSAGE"],
    "stream": os.environ["STREAM"] == "true",
}))')

# The first call cold-starts a session sandbox, which takes a few seconds.
curl -sS -N -X POST \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$BODY" \
    --max-time 300 \
    "$URL"
echo
