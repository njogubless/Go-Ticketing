#!/usr/bin/env bash
# Roll an ECS service back to an earlier task-definition revision.
#
#   rollback.sh --name NAME [--to-revision N] [--url URL] [--yes] [--force]
#   rollback.sh --name NAME --list
#
# NAME is the module's `name` (e.g. go-ticketing-sandbox). The module gives the
# cluster, the service and the task-definition family that same name, which is
# what lets this script work from nothing but AWS credentials — no Terraform,
# no state file, no checkout of the right commit. In an incident those are
# exactly the things you do not want to depend on.
#
# Default target: the newest ACTIVE revision older than the one the service is
# running now. Use --list to see revisions with their image tags, and
# --to-revision to pick one explicitly.
#
# What it does:
#   1. Refuses if a deployment is already IN_PROGRESS (the circuit breaker may
#      be mid-rollback already; two rollbacks racing helps nobody). --force
#      overrides.
#   2. Resolves the target revision and checks its image still exists in ECR —
#      the lifecycle policy expires old images, and a revision whose image is
#      gone would fail to start.
#   3. Points the service at the target revision. This is an ordinary rolling
#      deployment, with the same health-check gate and circuit breaker as a
#      forward deploy: a rollback cannot take the service down either.
#   4. Waits for that revision to reach COMPLETED (verify-deploy.sh).
#   5. Prints how to reconcile Terraform, whose state still names the newer
#      revision.
#
# Requires: aws CLI v2, jq.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
name="" to_revision="" url="" assume_yes=false force=false list=false timeout="${DEPLOY_TIMEOUT:-900}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --name) name="$2"; shift 2 ;;
    --to-revision) to_revision="$2"; shift 2 ;;
    --url) url="$2"; shift 2 ;;
    --timeout) timeout="$2"; shift 2 ;;
    --yes|-y) assume_yes=true; shift ;;
    --force) force=true; shift ;;
    --list) list=true; shift ;;
    -h|--help) sed -n '2,33p' "$0"; exit 0 ;;
    *) echo "rollback: unknown argument $1" >&2; exit 2 ;;
  esac
done
[[ -n "$name" ]] || { echo "usage: rollback.sh --name NAME [--to-revision N] [--list] [--yes]" >&2; exit 2; }
[[ -z "$to_revision" || "$to_revision" =~ ^[0-9]+$ ]] || { echo "rollback: --to-revision must be a number" >&2; exit 2; }

cluster="$name" service="$name" family="$name"

log() { printf '[rollback] %s\n' "$*"; }
die() { printf '[rollback] ERROR: %s\n' "$*" >&2; exit 1; }

revision_of() { echo "${1##*:}"; }

image_of() {
  aws ecs describe-task-definition --task-definition "$1" --output json \
    | jq -r '.taskDefinition.containerDefinitions[0].image'
}

# --- current state ------------------------------------------------------------

svc="$(aws ecs describe-services --cluster "$cluster" --services "$service" --output json)"
[[ "$(jq -r '.services | length' <<<"$svc")" == "1" ]] || die "service $cluster/$service not found"

current_arn="$(jq -r '.services[0].taskDefinition' <<<"$svc")"
current_rev="$(revision_of "$current_arn")"

# All ACTIVE revisions of exactly this family, newest first. --family-prefix is
# a prefix match, so "app" would also match "app-worker"; the regex keeps only
# this family.
revisions="$(aws ecs list-task-definitions --family-prefix "$family" --status ACTIVE --sort DESC --output json \
  | jq -r --arg f "$family" '.taskDefinitionArns[] | select(test("task-definition/" + $f + ":[0-9]+$"))')"

if $list; then
  log "ACTIVE revisions of $family (newest first):"
  while read -r arn; do
    [[ -n "$arn" ]] || continue
    marker="   "; [[ "$arn" == "$current_arn" ]] && marker="-> "
    printf '  %s%-6s %s\n' "$marker" "$(revision_of "$arn")" "$(image_of "$arn")"
  done <<<"$revisions"
  exit 0
fi

in_progress="$(jq '[.services[0].deployments[] | select(.rolloutState == "IN_PROGRESS")] | length' <<<"$svc")"
if [[ "$in_progress" != "0" ]] && ! $force; then
  die "a deployment is IN_PROGRESS on $service. If it is failing, the circuit breaker may already be rolling it back — wait for it, or pass --force."
fi

# --- choose the target --------------------------------------------------------

if [[ -n "$to_revision" ]]; then
  target_arn="$(grep -E ":${to_revision}$" <<<"$revisions" || true)"
  [[ -n "$target_arn" ]] || die "revision $to_revision of $family is not ACTIVE (deregistered or never existed)"
else
  target_arn=""
  while read -r arn; do
    [[ -n "$arn" ]] || continue
    if (( $(revision_of "$arn") < current_rev )); then target_arn="$arn"; break; fi
  done <<<"$revisions"
  [[ -n "$target_arn" ]] || die "no ACTIVE revision older than $current_rev — nothing to roll back to"
fi
target_rev="$(revision_of "$target_arn")"
[[ "$target_rev" != "$current_rev" ]] || die "service is already running revision $target_rev"

# --- the target's image must still exist --------------------------------------

current_image="$(image_of "$current_arn")"
target_image="$(image_of "$target_arn")"
repo_path="${target_image#*/}"   # strip registry host
repo="${repo_path%%:*}"
tag="${target_image##*:}"

if ! aws ecr describe-images --repository-name "$repo" --image-ids "imageTag=$tag" --output json >/dev/null 2>&1; then
  die "image $repo:$tag for revision $target_rev no longer exists in ECR (expired by the lifecycle policy?). Pick a newer revision with --list."
fi

# --- confirm & execute --------------------------------------------------------

log "service:  $cluster/$service"
log "current:  revision $current_rev  ($current_image)"
log "target:   revision $target_rev  ($target_image)"

if ! $assume_yes; then
  read -r -p "[rollback] proceed? [y/N] " answer
  [[ "$answer" =~ ^[Yy]$ ]] || die "aborted"
fi

aws ecs update-service --cluster "$cluster" --service "$service" \
  --task-definition "$target_arn" --output json >/dev/null
log "update-service issued; rolling deployment to revision $target_rev started"

verify_args=(--cluster "$cluster" --service "$service" --task-definition "$target_arn" --timeout "$timeout")
[[ -n "$url" ]] && verify_args+=(--url "$url")
"$SCRIPT_DIR/verify-deploy.sh" "${verify_args[@]}"

cat <<EOF
[rollback] DONE — $service is running revision $target_rev ($tag).

[rollback] Terraform state still names revision $current_rev, so the next
           \`terraform plan\` will show the service changing back to it. Before
           the next deploy, do one of:
             * fix forward: merge a fix and deploy the new commit as usual, or
             * re-pin:      run the deploy workflow with image_tag=$tag
                            (no rebuild; Terraform registers a new revision of the
                            good image and state matches reality again).
           Do NOT re-run the deploy of the bad commit.
EOF
