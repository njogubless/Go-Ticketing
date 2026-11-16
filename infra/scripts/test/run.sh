#!/usr/bin/env bash
# Tests for rollback.sh and verify-deploy.sh against a fake AWS CLI.
#
#   infra/scripts/test/run.sh
#
# These prove the decision logic — which revision is chosen, when the scripts
# refuse to act, how a failed or rolled-back deployment is detected — without
# an AWS account. What they cannot prove is that real AWS responds the way the
# fixtures say; the fixtures mirror the documented DescribeServices shape
# (deployments[].status / rolloutState / runningCount), and the first real
# rollback drill (docs/infra/README.md, "Rollback drill") closes that gap.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS="$(cd "$HERE/.." && pwd)"
F="go-ticketing-sandbox"
ARN="arn:aws:ecs:eu-west-1:123456789012:task-definition"
REGISTRY="123456789012.dkr.ecr.eu-west-1.amazonaws.com"

export POLL_INTERVAL=0
# Short, so a regression that stops a terminal state being recognised fails
# the test in seconds instead of spinning for the 15-minute default.
export DEPLOY_TIMEOUT=3
passed=0 failed=0

# --- fixture builders ---------------------------------------------------------

# dep REV STATUS ROLLOUT RUNNING
dep() {
  printf '{"taskDefinition":"%s/%s:%s","status":"%s","rolloutState":"%s","rolloutStateReason":"fixture","runningCount":%s,"desiredCount":1}' \
    "$ARN" "$F" "$1" "$2" "$3" "$4"
}

# services N CURRENT_REV DEP_JSON...  -> describe-services.N.json
services() {
  local n="$1" current="$2"; shift 2
  local deps; deps="$(IFS=,; echo "$*")"
  cat >"$SCENARIO/describe-services.$n.json" <<EOF
{"services":[{"serviceName":"$F","taskDefinition":"$ARN/$F:$current","deployments":[$deps],
 "events":[{"createdAt":"2026-11-16T10:00:00Z","message":"(service $F) fixture event"}]}]}
EOF
}

# revisions REV...  -> list-task-definitions.json and a task-def file per
# revision. Look-alike families (go-ticketing-sandbox2, -worker) are listed
# FIRST with revision 2, so a script that matched on prefix alone would pick
# one of them before ever reaching the real older revision.
revisions() {
  local arns=("\"$ARN/${F}2:2\"" "\"$ARN/${F}-worker:2\"") rev
  for rev in "$@"; do
    arns+=("\"$ARN/$F:$rev\"")
    cat >"$SCENARIO/task-def-$rev.json" <<EOF
{"taskDefinition":{"containerDefinitions":[{"image":"$REGISTRY/$F:sha-$rev"}]}}
EOF
  done
  echo "{\"taskDefinitionArns\":[$(IFS=,; echo "${arns[*]}")]}" >"$SCENARIO/list-task-definitions.json"
}

# --- harness ------------------------------------------------------------------

# Each test runs in a fresh scenario directory with the fake CLI first on PATH.
run_test() {
  local name="$1"
  SCENARIO="$(mktemp -d)"
  mkdir -p "$SCENARIO/bin"
  ln -s "$HERE/fake-aws" "$SCENARIO/bin/aws"
  export FAKE_AWS_DIR="$SCENARIO" FAKE_AWS_CALLS="$SCENARIO/calls.log"
  : >"$FAKE_AWS_CALLS"
  OUT="$SCENARIO/out.log"

  if (PATH="$SCENARIO/bin:$PATH"; "$name") >"$SCENARIO/test.log" 2>&1; then
    printf '  ok    %s\n' "$name"; passed=$((passed + 1))
  else
    printf '  FAIL  %s\n' "$name"; failed=$((failed + 1))
    sed 's/^/        /' "$SCENARIO/test.log"
  fi
  rm -rf "$SCENARIO"
}

# Assertions `exit` rather than `return`: tests run inside `if ( ... )`, and
# bash disables `set -e` in that context, so a failed assertion that merely
# returned would be ignored and only the test's last line would count.

# Run a script, capture output and exit code without tripping `set -e`.
invoke() { set +e; "$@" >"$OUT" 2>&1; CODE=$?; set -e; }

assert_code()     { [[ "$CODE" == "$1" ]] || { echo "expected exit $1, got $CODE"; cat "$OUT"; exit 1; }; }
assert_nonzero()  { [[ "$CODE" != "0" ]] || { echo "expected failure, got exit 0"; cat "$OUT"; exit 1; }; }
assert_output()   { grep -qF -- "$1" "$OUT" || { echo "output missing: $1"; cat "$OUT"; exit 1; }; }
assert_called()   { grep -qF -- "$1" "$FAKE_AWS_CALLS" || { echo "aws not called with: $1"; cat "$FAKE_AWS_CALLS"; exit 1; }; }
assert_not_called() { ! grep -qF -- "$1" "$FAKE_AWS_CALLS" || { echo "aws unexpectedly called with: $1"; cat "$FAKE_AWS_CALLS"; exit 1; }; }

# --- rollback.sh --------------------------------------------------------------

test_rollback_defaults_to_previous_revision() {
  revisions 7 6 5
  services 1 7 "$(dep 7 PRIMARY COMPLETED 1)"
  services 2 6 "$(dep 6 PRIMARY IN_PROGRESS 0)" "$(dep 7 ACTIVE COMPLETED 1)"
  services 3 6 "$(dep 6 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --yes
  assert_code 0
  assert_called "ecs update-service --cluster $F --service $F --task-definition $ARN/$F:6"
  assert_output "rollout COMPLETED for $F:6"
  assert_output "image_tag=sha-6"
}

test_rollback_to_explicit_revision() {
  revisions 7 6 5
  services 1 7 "$(dep 7 PRIMARY COMPLETED 1)"
  services 2 5 "$(dep 5 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --to-revision 5 --yes
  assert_code 0
  assert_called "--task-definition $ARN/$F:5"
}

test_rollback_ignores_lookalike_families() {
  # The listing starts with go-ticketing-sandbox2:2 and -worker:2, both older
  # than the current revision 3; only the real family's revision 1 is valid.
  revisions 3 1
  services 1 3 "$(dep 3 PRIMARY COMPLETED 1)"
  services 2 1 "$(dep 1 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --yes
  assert_code 0
  assert_called "--task-definition $ARN/$F:1"
  assert_not_called "${F}2:2"
  assert_not_called "${F}-worker:2"
}

test_rollback_refuses_when_image_expired() {
  revisions 7 6
  echo "sha-6" >"$SCENARIO/missing-images"
  services 1 7 "$(dep 7 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --yes
  assert_nonzero
  assert_output "no longer exists in ECR"
  assert_not_called "update-service"
}

test_rollback_refuses_during_in_progress_deployment() {
  revisions 7 6
  services 1 7 "$(dep 7 PRIMARY IN_PROGRESS 0)" "$(dep 6 ACTIVE COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --yes
  assert_nonzero
  assert_output "IN_PROGRESS"
  assert_not_called "update-service"
}

test_rollback_force_overrides_in_progress_guard() {
  revisions 7 6
  services 1 7 "$(dep 7 PRIMARY IN_PROGRESS 0)" "$(dep 6 ACTIVE COMPLETED 1)"
  services 2 6 "$(dep 6 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --yes --force
  assert_code 0
  assert_called "--task-definition $ARN/$F:6"
}

test_rollback_fails_when_its_own_deployment_fails() {
  revisions 7 6
  services 1 7 "$(dep 7 PRIMARY COMPLETED 1)"
  services 2 6 "$(dep 6 PRIMARY FAILED 0)" "$(dep 7 ACTIVE COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --yes
  assert_nonzero
  assert_output "rollout FAILED"
}

test_rollback_with_no_older_revision() {
  revisions 5
  services 1 5 "$(dep 5 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --yes
  assert_nonzero
  assert_output "nothing to roll back to"
  assert_not_called "update-service"
}

test_rollback_rejects_inactive_revision() {
  revisions 7 6
  services 1 7 "$(dep 7 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --to-revision 3 --yes
  assert_nonzero
  assert_output "is not ACTIVE"
  assert_not_called "update-service"
}

test_rollback_without_yes_aborts_on_no() {
  revisions 7 6
  services 1 7 "$(dep 7 PRIMARY COMPLETED 1)"
  set +e; echo n | "$SCRIPTS/rollback.sh" --name "$F" >"$OUT" 2>&1; CODE=$?; set -e
  assert_nonzero
  assert_output "aborted"
  assert_not_called "update-service"
}

test_rollback_list_marks_current_revision() {
  revisions 7 6 5
  services 1 6 "$(dep 6 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/rollback.sh" --name "$F" --list
  assert_code 0
  assert_output "-> 6      $REGISTRY/$F:sha-6"
  assert_output "7      $REGISTRY/$F:sha-7"
  assert_not_called "update-service"
}

# --- verify-deploy.sh ---------------------------------------------------------

test_verify_succeeds_on_completed_primary() {
  services 1 8 "$(dep 8 PRIMARY IN_PROGRESS 0)" "$(dep 7 ACTIVE COMPLETED 1)"
  services 2 8 "$(dep 8 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/verify-deploy.sh" --cluster "$F" --service "$F" --task-definition "$ARN/$F:8"
  assert_code 0
  assert_output "deployment verified"
}

test_verify_detects_circuit_breaker_rollback() {
  # The bad revision 8 fails; ECS marks it FAILED and starts redeploying 7.
  services 1 8 "$(dep 8 PRIMARY IN_PROGRESS 0)" "$(dep 7 ACTIVE COMPLETED 1)"
  services 2 7 "$(dep 7 PRIMARY IN_PROGRESS 1)" "$(dep 8 ACTIVE FAILED 0)"
  invoke "$SCRIPTS/verify-deploy.sh" --cluster "$F" --service "$F" --task-definition "$ARN/$F:8"
  assert_nonzero
  assert_output "rollout FAILED"
  assert_output "fixture event" # service events are printed for diagnosis
}

test_verify_detects_deployment_that_disappeared() {
  # Rolled back and already cleaned up between two polls.
  services 1 8 "$(dep 8 PRIMARY IN_PROGRESS 0)" "$(dep 7 ACTIVE COMPLETED 1)"
  services 2 7 "$(dep 7 PRIMARY COMPLETED 1)"
  invoke "$SCRIPTS/verify-deploy.sh" --cluster "$F" --service "$F" --task-definition "$ARN/$F:8"
  assert_nonzero
  assert_output "disappeared; service is now on $F:7"
}

test_verify_does_not_accept_stable_on_old_revision() {
  # What `aws ecs wait services-stable` would wrongly call success: the service
  # is stable, but on revision 7, and 8 was demoted.
  services 1 7 "$(dep 7 PRIMARY COMPLETED 1)" "$(dep 8 ACTIVE IN_PROGRESS 0)"
  invoke "$SCRIPTS/verify-deploy.sh" --cluster "$F" --service "$F" --task-definition "$ARN/$F:8"
  assert_nonzero
  assert_output "no longer the primary deployment"
}

test_verify_times_out() {
  services 1 8 "$(dep 8 PRIMARY IN_PROGRESS 0)" "$(dep 7 ACTIVE COMPLETED 1)"
  invoke "$SCRIPTS/verify-deploy.sh" --cluster "$F" --service "$F" --task-definition "$ARN/$F:8" --timeout 0
  assert_nonzero
  assert_output "timed out"
}

echo "rollback.sh / verify-deploy.sh"
for t in $(declare -F | awk '{print $3}' | grep '^test_'); do
  run_test "$t"
done
echo "$passed passed, $failed failed"
(( failed == 0 ))
