#!/usr/bin/env bash
# sa-deploy-approve.sh — legt ein Promotion-Approval für `deploy promote` an
# (sa-deploy-stufen W3, Approval-Store-Fallback). Schreibt die root-only
# JSON-Datei, die das Gate liest. SENDET NICHTS nach WhatsApp — das Erinnern
# läuft über die bestehende WA-Approval-Schiene (präziser Folge-Punkt, s. PRD).
#
# Aufruf:  sa-deploy-approve <app> <sha> <approver> [ttl-hours]
#   <app>       Ledger-Service-Name (z.B. staging-canary)
#   <sha>       exakte, auf Staging bewiesene git_sha (volle 40 Zeichen)
#   <approver>  wer freigibt (z.B. mario)
#   [ttl-hours] Gültigkeit in Stunden (Default 24). Danach ist die Freigabe
#               abgelaufen und das Gate verweigert erneut.
# Env: DEPLOY_APPROVAL_DIR (Default /etc/sa-deploy/approvals)
set -euo pipefail

DIR="${DEPLOY_APPROVAL_DIR:-/etc/sa-deploy/approvals}"

if [[ $# -lt 3 || $# -gt 4 ]]; then
  echo "sa-deploy-approve: brauche <app> <sha> <approver> [ttl-hours], bekam $#" >&2
  exit 64
fi
APP="$1"; SHA="$2"; APPROVER="$3"; TTL_HOURS="${4:-24}"

[[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || { echo "sa-deploy-approve: <sha> muss eine volle 40-stellige git_sha sein, war: $SHA" >&2; exit 64; }
[[ "$TTL_HOURS" =~ ^[0-9]+$ && "$TTL_HOURS" -gt 0 ]] || { echo "sa-deploy-approve: ttl-hours muss eine positive Ganzzahl sein, war: $TTL_HOURS" >&2; exit 64; }

if [[ $EUID -ne 0 ]]; then
  echo "sa-deploy-approve: muss als root laufen (Store ist root-only)" >&2
  exit 77
fi

mkdir -p "$DIR"; chmod 700 "$DIR"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
TTL_SECONDS=$(( TTL_HOURS * 3600 ))
FILE="$DIR/${APP}-${SHA}.json"
NOTE="${SA_APPROVE_NOTE:-Freigabe via sa-deploy-approve}"

umask 177
cat > "$FILE" <<JSON
{
  "app": "${APP}",
  "sha": "${SHA}",
  "approved_by": "${APPROVER}",
  "approved_at": "${NOW}",
  "ttl_seconds": ${TTL_SECONDS},
  "note": "${NOTE}"
}
JSON
chmod 600 "$FILE"
echo "ok: Approval ${APP}@${SHA:0:12} von ${APPROVER}, gültig ${TTL_HOURS}h → ${FILE}"
