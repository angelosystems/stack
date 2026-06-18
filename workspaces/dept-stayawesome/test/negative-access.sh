#!/usr/bin/env bash
# Negative test — must run INSIDE a provisioned tenant workspace.
#
# Acceptance (E4 from tenant-workspaces-prd.md §11):
#   /opt/quantbot, the global Vault, and other tenants' repos are NOT
#   reachable from this workspace.
#
# Exit codes:
#   0 — all forbidden paths confirmed unreachable (negative test green)
#   1 — at least one forbidden path was reachable (FAIL)

set -u

fail=0
checks=0

forbidden=(
  /opt/quantbot
  /opt/quantbot-pg
  /root/.secrets
  /opt/vault
  /srv/tenants/dept-finance/repos
  /srv/tenants/dept-engineering/repos
)

for path in "${forbidden[@]}"; do
  checks=$((checks + 1))
  if [ -e "$path" ] || [ -r "$path" ]; then
    echo "FAIL: forbidden path is reachable from workspace: $path"
    fail=1
  else
    echo "ok:   $path unreachable"
  fi
done

# Sanity: PLAN_REPO_ROOTS must be set and point at the tenant mount only.
checks=$((checks + 1))
if [ -z "${PLAN_REPO_ROOTS:-}" ]; then
  echo "FAIL: PLAN_REPO_ROOTS is unset"
  fail=1
elif [ "$PLAN_REPO_ROOTS" != "/home/coder/repos" ]; then
  echo "FAIL: PLAN_REPO_ROOTS not scoped to tenant mount (got: $PLAN_REPO_ROOTS)"
  fail=1
else
  echo "ok:   PLAN_REPO_ROOTS=$PLAN_REPO_ROOTS"
fi

echo
if [ "$fail" -eq 0 ]; then
  echo "PASS — $checks checks, all forbidden paths unreachable"
  exit 0
else
  echo "FAIL — $checks checks, see above"
  exit 1
fi
