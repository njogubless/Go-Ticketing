#!/usr/bin/env bash
# Assert deployment-safety invariants on a `terraform show -json` plan.
#
#   infra/scripts/check-plan.sh plan.json
#
# These are the properties the rollback story depends on. Each is one line of
# Terraform that a well-meaning refactor could delete without any test failing;
# this script makes deleting them fail CI instead.
set -euo pipefail

PLAN="${1:?usage: check-plan.sh plan.json}"
failures=0

# after-state of the first planned resource of a given type.
after() { jq -c --arg t "$1" '[.resource_changes[] | select(.type == $t) | .change.after][0]' "$PLAN"; }

check() {
  local description="$1" filter="$2" json="$3"
  if [[ "$(jq -r "$filter" <<<"$json")" == "true" ]]; then
    printf '  ok    %s\n' "$description"
  else
    printf '  FAIL  %s\n' "$description"
    failures=$((failures + 1))
  fi
}

service="$(after aws_ecs_service)"
taskdef="$(after aws_ecs_task_definition)"
ecr="$(after aws_ecr_repository)"
tg="$(after aws_lb_target_group)"
db="$(after aws_db_instance)"

check "ECS circuit breaker enabled with automatic rollback" \
  '.deployment_circuit_breaker[0].enable == true and .deployment_circuit_breaker[0].rollback == true' "$service"
check "minimum healthy percent >= 100 (old task outlives new until healthy)" \
  '.deployment_minimum_healthy_percent >= 100' "$service"
check "maximum percent leaves room for a replacement task" \
  '.deployment_maximum_percent > 100' "$service"
check "alarm-based rollback wired (if alarms configured)" \
  '(.alarms | length) == 0 or (.alarms[0].rollback == true and .alarms[0].enable == true)' "$service"
check "task definitions are kept on replacement (skip_destroy)" \
  '.skip_destroy == true' "$taskdef"
check "image_tag is not \"latest\"" \
  '.variables.image_tag.value != "latest"' "$(cat "$PLAN")"
check "ECR tags are immutable" \
  '.image_tag_mutability == "IMMUTABLE"' "$ecr"
check "target group has a health check path" \
  '.health_check[0].path | startswith("/")' "$tg"

if [[ "$db" != "null" ]]; then
  check "database is not publicly accessible" '.publicly_accessible == false' "$db"
  check "database storage is encrypted" '.storage_encrypted == true' "$db"
  check "database takes a final snapshot on delete" '.skip_final_snapshot == false' "$db"
fi

if ((failures > 0)); then
  echo "check-plan: $failures invariant(s) violated" >&2
  exit 1
fi
echo "check-plan: all invariants hold"
