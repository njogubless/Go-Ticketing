#!/usr/bin/env bash
# Wait for one specific ECS task-definition revision to finish rolling out, and
# fail loudly if it does not.
#
#   verify-deploy.sh --cluster C --service S --task-definition ARN [--url URL] [--timeout SECONDS]
#
# Exit 0  — the revision is the service's PRIMARY deployment, rolloutState is
#           COMPLETED, all desired tasks are running, and (with --url) both
#           /healthz and /readyz answer 200 through the load balancer.
# Exit 1  — the deployment FAILED (circuit breaker or alarm rollback), was
#           superseded, never appeared, or did not finish within the timeout.
#
# Why not `aws ecs wait services-stable`: it gives up after 10 minutes, and it
# reports success when the service is stable on *any* revision — including the
# old one the circuit breaker just rolled back to. That is the exact case this
# script exists to catch.
#
# Requires: aws CLI v2, jq, curl (with --url).
set -euo pipefail

POLL_INTERVAL="${POLL_INTERVAL:-15}"
cluster="" service="" taskdef="" url="" timeout="${DEPLOY_TIMEOUT:-900}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster) cluster="$2"; shift 2 ;;
    --service) service="$2"; shift 2 ;;
    --task-definition) taskdef="$2"; shift 2 ;;
    --url) url="$2"; shift 2 ;;
    --timeout) timeout="$2"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "verify-deploy: unknown argument $1" >&2; exit 2 ;;
  esac
done
[[ -n "$cluster" && -n "$service" && -n "$taskdef" ]] || {
  echo "usage: verify-deploy.sh --cluster C --service S --task-definition ARN [--url URL] [--timeout S]" >&2
  exit 2
}

log() { printf '[verify-deploy] %s\n' "$*"; }

recent_events() {
  aws ecs describe-services --cluster "$cluster" --services "$service" --output json \
    | jq -r '.services[0].events[:8][] | "  \(.createdAt)  \(.message)"' || true
}

fail() {
  log "FAILED: $*"
  log "most recent service events:"
  recent_events
  exit 1
}

log "waiting for ${taskdef##*/} on $cluster/$service (timeout ${timeout}s)"
deadline=$(( $(date +%s) + timeout ))
seen=false

while :; do
  svc="$(aws ecs describe-services --cluster "$cluster" --services "$service" --output json)"
  ours="$(jq -c --arg td "$taskdef" '[.services[0].deployments[] | select(.taskDefinition == $td)][0]' <<<"$svc")"

  if [[ "$ours" == "null" ]]; then
    if $seen; then
      # It existed and is gone: ECS replaced it, which only happens when it was
      # rolled back or a newer deployment superseded it.
      primary="$(jq -r '.services[0].deployments[] | select(.status == "PRIMARY") | .taskDefinition' <<<"$svc")"
      fail "deployment of ${taskdef##*/} disappeared; service is now on ${primary##*/} (rolled back or superseded)"
    fi
  else
    seen=true
    status="$(jq -r '.status' <<<"$ours")"
    rollout="$(jq -r '.rolloutState // "UNKNOWN"' <<<"$ours")"
    running="$(jq -r '.runningCount' <<<"$ours")"
    desired="$(jq -r '.desiredCount' <<<"$ours")"
    log "status=$status rollout=$rollout running=$running/$desired"

    case "$rollout" in
      FAILED)
        fail "rollout FAILED: $(jq -r '.rolloutStateReason // "no reason given"' <<<"$ours")" ;;
      COMPLETED)
        if [[ "$status" == "PRIMARY" && "$running" == "$desired" ]]; then
          break
        fi ;;
    esac

    if [[ "$status" != "PRIMARY" ]]; then
      # Still listed but no longer primary: a rollback deployment has taken over.
      fail "${taskdef##*/} is no longer the primary deployment (status=$status) — ECS is rolling back"
    fi
  fi

  if (( $(date +%s) >= deadline )); then
    fail "timed out after ${timeout}s"
  fi
  sleep "$POLL_INTERVAL"
done

log "rollout COMPLETED for ${taskdef##*/}"

if [[ -n "$url" ]]; then
  for path in /healthz /readyz; do
    ok=false
    for _ in 1 2 3 4 5; do
      if curl -sf --max-time 5 "${url%/}$path" >/dev/null; then ok=true; break; fi
      sleep "$POLL_INTERVAL"
    done
    $ok || fail "${url%/}$path did not return 200 after the rollout completed"
    log "${url%/}$path OK"
  done
fi

log "deployment verified"
