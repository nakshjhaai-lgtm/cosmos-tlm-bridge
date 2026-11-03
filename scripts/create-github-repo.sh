#!/usr/bin/env bash
# Create the clean public repository and apply the five required topics.
# Run after reconnecting GitHub in Arena with repository-creation permission.
set -euo pipefail
REPO="nakshjhaai-lgtm/cosmos-tlm-bridge"
DESCRIPTION="OpenC3 COSMOS v5 telemetry-to-microservice bridge and CI regression runner"

if gh repo view "$REPO" >/dev/null 2>&1; then
  echo "Repository $REPO already exists; applying topics only."
else
  gh repo create "$REPO" \
    --public \
    --description "$DESCRIPTION" \
    --source . \
    --remote cosmos \
    --push
fi

for topic in openc3 cosmos satellite-telemetry ground-software satellite-operations; do
  gh repo edit "$REPO" --add-topic "$topic"
done

gh repo view "$REPO" --json name,visibility,repositoryTopics,url
