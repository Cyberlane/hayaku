#!/bin/sh
# Reference integration: retain the full suite as the CI gate.
set -eu
: "${HAYAKU_BASE:?Set a locally available comparison commit}"
: "${HAYAKU_CANDIDATE:?Set the exact tested commit, including merge results}"
HAYAKU_CONFIG=${HAYAKU_CONFIG:-hayaku.json}
HAYAKU_BIN=${HAYAKU_BIN:-hayaku}
HAYAKU_ARTIFACT_DIR=${HAYAKU_ARTIFACT_DIR:-.git/hayaku-ci}
mkdir -p "$HAYAKU_ARTIFACT_DIR"
# Artifact files are exclusive-created: choose a fresh directory for each job.
"$HAYAKU_BIN" plan --base "$HAYAKU_BASE" --candidate "$HAYAKU_CANDIDATE" --config "$HAYAKU_CONFIG" --output "$HAYAKU_ARTIFACT_DIR/plan.json"
if [ "${HAYAKU_FORCE_FULL:-0}" != "1" ]; then
  # A shadow miss or invalid comparison fails the job; never ignore its status.
  "$HAYAKU_BIN" shadow --plan "$HAYAKU_ARTIFACT_DIR/plan.json" --config "$HAYAKU_CONFIG" --output "$HAYAKU_ARTIFACT_DIR/shadow.json"
fi
"$HAYAKU_BIN" run --plan "$HAYAKU_ARTIFACT_DIR/plan.json" --config "$HAYAKU_CONFIG" --output "$HAYAKU_ARTIFACT_DIR/full.json"
