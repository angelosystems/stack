#!/bin/bash
# ledger-record.sh — Release-Ledger-Zeile bei Deploy-START (Release-Pipeline WP3).
# Gemeinsamer Baustein aller deploy.sh (master-kanban + Adapter): schreibt die
# 'deploying'-Zeile, die die 60-s-Probe (Reconciler) nach dem Restart auf
# 'live' bestätigt. Kein Ledger-Eintrag ⇒ kein Deploy (Aufrufer läuft mit
# set -e); Not-Ausstieg für Ledger-Ausfälle: LEDGER_SKIP=1.
#
# Aufruf:  ledger-record.sh <service> <http|cli> <version> <git_sha> <health_url>
#   health_url: http → /version-URL · cli → ABSOLUTER Binary-Pfad (Reconciler
#   ruft `<pfad> version --json`).
# Env:
#   PORTFOLIO_DSN       Ledger-DB (Default Master-Kanban :5434)
#   MK_ENV              environment (Default prod-mvp, D9-Identitäts-Key)
#   DEPLOY_ACTOR        deployed_by (Default manual@<host>)
#   DEPLOY_METHOD       deploy_method (Default deploy-sh)
#   DEPLOY_INITIATIVE   initiative_id fürs Board (optional)
#   DEPLOY_PREV_VERSION Rollback-Ziel; leer ⇒ letzte live-Version aus dem Ledger
#
# Semantik: Upsert auf (service,environment,git_sha) = Idempotenz D11 — ein
# Re-Deploy derselben SHA trifft dieselbe Zeile. status='rolled_back' wird NIE
# überschrieben (Gift-SHA-Quarantäne D15): exit 1 mit Handlungsanweisung;
# Freigeben ist ein bewusster menschlicher UPDATE, kein Deploy-Nebeneffekt.
set -euo pipefail

if [ $# -ne 5 ]; then
  echo "ledger-record.sh: brauche <service> <http|cli> <version> <git_sha> <health_url>, bekam $# Argumente" >&2
  exit 2
fi
SERVICE="$1"; PROBE_KIND="$2"; VERSION="$3"; SHA="$4"; HEALTH_URL="$5"

if [ "${LEDGER_SKIP:-0}" = "1" ]; then
  echo "LEDGER_SKIP=1 → Deploy OHNE Ledger-Zeile (das Board weiß davon nichts!)" >&2
  exit 0
fi
case "$PROBE_KIND" in
  http|cli) ;;
  *) echo "ledger-record.sh: probe_kind muss http oder cli sein, war: $PROBE_KIND" >&2; exit 2 ;;
esac
if [ "$PROBE_KIND" = "cli" ] && [ "${HEALTH_URL:0:1}" != "/" ]; then
  echo "ledger-record.sh: cli-health_url muss absoluter Binary-Pfad sein, war: $HEALTH_URL" >&2
  exit 2
fi

PORTFOLIO_DSN="${PORTFOLIO_DSN:-postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable}"
ENVIRONMENT="${MK_ENV:-prod-mvp}"
DEPLOYED_BY="${DEPLOY_ACTOR:-manual@$(hostname)}"
METHOD="${DEPLOY_METHOD:-deploy-sh}"
INITIATIVE="${DEPLOY_INITIATIVE:-}"

PREV_VERSION="${DEPLOY_PREV_VERSION:-}"
if [ -z "$PREV_VERSION" ]; then
  PREV_VERSION=$(psql "$PORTFOLIO_DSN" -Atqc "SELECT version FROM portfolio.deployments
    WHERE service='${SERVICE}' AND environment='${ENVIRONMENT}' AND status='live'
    ORDER BY deployed_at DESC LIMIT 1" 2>/dev/null || true)
fi

LEDGER_ID=$(psql "$PORTFOLIO_DSN" -v ON_ERROR_STOP=1 -Atq \
  -v svc="$SERVICE" -v pk="$PROBE_KIND" -v env="$ENVIRONMENT" -v ver="$VERSION" -v sha="$SHA" \
  -v prev="$PREV_VERSION" -v by="$DEPLOYED_BY" -v meth="$METHOD" -v init="$INITIATIVE" -v hurl="$HEALTH_URL" <<'SQL'
INSERT INTO portfolio.deployments
  (service, probe_kind, environment, version, git_sha, initiative_id,
   status, deployed_by, deploy_method, prev_version, health_url)
VALUES
  (:'svc', :'pk', :'env', :'ver', :'sha', NULLIF(:'init',''),
   'deploying', :'by', :'meth', NULLIF(:'prev',''), :'hurl')
ON CONFLICT (service, environment, git_sha) DO UPDATE
  SET status='deploying', deployed_at=now(), version=EXCLUDED.version,
      deployed_by=EXCLUDED.deployed_by, prev_version=EXCLUDED.prev_version,
      probe_kind=EXCLUDED.probe_kind, health_url=EXCLUDED.health_url
  WHERE portfolio.deployments.status <> 'rolled_back'
RETURNING id
SQL
)
if [ -z "$LEDGER_ID" ]; then
  echo "ABBRUCH: ${SHA} ist für ${SERVICE}@${ENVIRONMENT} quarantänisiert (status=rolled_back, D15)." >&2
  echo "Bewusst freigeben:  psql \"\$PORTFOLIO_DSN\" -c \"UPDATE portfolio.deployments SET status='errored' WHERE service='${SERVICE}' AND environment='${ENVIRONMENT}' AND git_sha='${SHA}'\"  — dann erneut deployen." >&2
  exit 1
fi
echo "Ledger: portfolio.deployments #${LEDGER_ID} ← ${SERVICE}@${ENVIRONMENT} ${VERSION} (${SHA}) status=deploying"
