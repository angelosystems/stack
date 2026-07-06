#!/usr/bin/env bash
# deploy-reactor-proof.sh — WP5-Beweis: EIN human-getriggerter (--once) Grün-
# Durchlauf des Outbox-Deploy-Reaktors auf der harmlosesten Service-Klasse
# (master-kanban, cli-Probe), vollständig isoliert.
#
# Isolation (wie Session As test-gate-proof.sh, GitHub-frei, restlos aufgeräumt):
#   * Scratch-Ledger-DB (deploy_proof_d) — nur portfolio.deployments, danach gedroppt.
#   * Scratch-Binary-Pfad — der Live-Stand /opt/stack/bin/master-kanban und
#     :7780 werden NIE angefasst (kein Swap, kein Restart).
#   * Reaktor-Binary frisch aus dem Arbeitsbaum gebaut (mein WP5-Code), NICHT
#     das Live-Binary.
#
# Beweist die PRD-Akzeptanz #3: bare Outbox-Zeile (nur service/env/sha, wie der
# Merger sie schreibt) → Reaktor claim→deploying→live, GENAU eine Zeile je
# (service,git_sha), idempotent bei Doppel-Zustellung, laufendes version==SHA.
#
# Aufruf:  deploy-reactor-proof.sh          (baut, beweist, räumt auf)
#          KEEP=1 deploy-reactor-proof.sh   (Scratch-DB/-Dirs stehen lassen)

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

REPO="/opt/stack"
SRC_REL="tools/portfolio/master-kanban"   # relativ zum Repo (deploy-gt.sh --src)
SRC_ABS="$REPO/$SRC_REL"                   # absolut (Build/sed im Skript)
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/deploy-proof.XXXXXX")"
SCRATCH_DB="deploy_proof_d"
PASS=0; TOTAL=0
say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { TOTAL=$((TOTAL+1)); PASS=$((PASS+1)); printf '\033[1;32m  ok\033[0m %s\n' "$*"; }
miss() { TOTAL=$((TOTAL+1)); printf '\033[1;31m  MISS\033[0m %s\n' "$*"; }

# DSN zur Laufzeit aus dem Repo-Default extrahieren — kein Klartext-Secret hier.
BASE_DSN="$(sed -n 's/.*envOr("PORTFOLIO_DSN", "\([^"]*\)".*/\1/p' "$SRC_ABS/main.go" | head -1)"
[[ -n "$BASE_DSN" ]] || { echo "DSN nicht auffindbar" >&2; exit 1; }
ADMIN_DSN="$BASE_DSN"
SCRATCH_DSN="$(echo "$BASE_DSN" | sed -E "s#(127.0.0.1:[0-9]+/)[^?]+#\1${SCRATCH_DB}#")"
q()  { psql "$SCRATCH_DSN" -Atqc "$1"; }

cleanup() {
    [[ "${KEEP:-0}" = "1" ]] && { say "KEEP=1 → Scratch bleibt: DB=$SCRATCH_DB DIR=$SCRATCH"; return; }
    psql "$ADMIN_DSN" -Atqc "DROP DATABASE IF EXISTS $SCRATCH_DB" >/dev/null 2>&1 || true
    rm -rf "$SCRATCH"
    git -C "$REPO" worktree prune >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Aufbau ───────────────────────────────────────────────────────────────────
say "Reaktor-Binary aus dem Arbeitsbaum bauen (WP5-Code)"
( cd "$SRC_ABS" && go build -o "$SCRATCH/mk" . )

say "Scratch-Ledger-DB $SCRATCH_DB anlegen (nur portfolio.deployments)"
psql "$ADMIN_DSN" -Atqc "DROP DATABASE IF EXISTS $SCRATCH_DB" >/dev/null
psql "$ADMIN_DSN" -Atqc "CREATE DATABASE $SCRATCH_DB" >/dev/null
psql "$SCRATCH_DSN" -v ON_ERROR_STOP=1 >/dev/null <<'SQL'
CREATE SCHEMA IF NOT EXISTS portfolio;
CREATE TABLE portfolio.deployments (
  id bigserial PRIMARY KEY, service text NOT NULL,
  probe_kind text NOT NULL DEFAULT 'http', environment text NOT NULL DEFAULT 'prod-mvp',
  version text, git_sha text NOT NULL, migration_version text, prev_migration_version text,
  config_sha text, initiative_id text, bead_ids text[],
  status text NOT NULL DEFAULT 'deploying', owned_by text, owned_until timestamptz,
  deployed_at timestamptz NOT NULL DEFAULT now(), deployed_by text NOT NULL DEFAULT 'manual',
  deploy_method text, prev_version text, log_url text, health_url text
);
CREATE UNIQUE INDEX deployments_service_env_sha_key ON portfolio.deployments (service, environment, git_sha);
SQL

SHA="$(git -C "$REPO" rev-parse HEAD)"       # voll — wie ein Merge-Commit-SHA
SHORT="$(git -C "$REPO" rev-parse --short HEAD)"
VBIN="$SCRATCH/master-kanban"                # Scratch-Ziel-Binary (cli-Service)

cat > "$SCRATCH/manifest.yaml" <<YAML
repo: $REPO
services:
  master-kanban:
    src: $SRC_REL
    bin: $VBIN
    unit: ""
    probe_kind: cli
    health_url: $VBIN
YAML

reactor() {
    SMOKE_FORCE_RED="${SMOKE_FORCE_RED:-}" "$SCRATCH/mk" deploy-reactor-outbox --once \
        --dsn "$SCRATCH_DSN" --manifest "$SCRATCH/manifest.yaml" \
        --script "$REPO/tools/portfolio/deploy-gt.sh" \
        --state-dir "$SCRATCH/state" --events-dir "$SCRATCH/events" \
        --smoke-sleep 200ms --max-smoke 20
}

# ── Lauf A: bare Outbox-Zeile → live ─────────────────────────────────────────
say "Lauf A — Outbox-Zeile (service/env/sha) schreiben, wie der Merger es tut"
q "INSERT INTO portfolio.deployments (service, environment, git_sha, status)
   VALUES ('master-kanban','prod-mvp','$SHA','pending')" >/dev/null

say "Reaktor --once (claim → deploy-gt.sh SHA-gepinnt → cli-Smoke → live)"
reactor

ST="$(q "SELECT status FROM portfolio.deployments WHERE git_sha='$SHA'")"
CNT="$(q "SELECT count(*) FROM portfolio.deployments WHERE git_sha='$SHA'")"
[[ "$ST" = "live" ]] && ok "Zeile ist live (deploying→live)" || miss "Status=$ST (erwartet live)"
[[ "$CNT" = "1" ]]  && ok "genau EINE Zeile je (service,git_sha)" || miss "count=$CNT (erwartet 1)"

VER_SHA="$("$VBIN" version --json 2>/dev/null | sed -n 's/.*"sha":"\([^"]*\)".*/\1/p')"
[[ "$VER_SHA" = "$SHORT" ]] && ok "laufendes version --json sha=$VER_SHA == deploy-SHA" \
    || miss "version-sha=$VER_SHA erwartet $SHORT"

LEASE="$(q "SELECT COALESCE(owned_by,'—') FROM portfolio.deployments WHERE git_sha='$SHA'")"
[[ "$LEASE" = "—" ]] && ok "Lease nach live freigegeben (owned_by NULL)" || miss "owned_by=$LEASE (Lease nicht frei)"

# ── Lauf B: Doppel-Zustellung → idempotent ───────────────────────────────────
say "Lauf B — zweiter --once (simulierte Doppel-Zustellung des Grün-Events)"
reactor
CNT2="$(q "SELECT count(*) FROM portfolio.deployments WHERE git_sha='$SHA'")"
ST2="$(q "SELECT status FROM portfolio.deployments WHERE git_sha='$SHA'")"
[[ "$CNT2" = "1" && "$ST2" = "live" ]] && ok "idempotent — weiterhin 1 Zeile, live (D11)" \
    || miss "nach Re-Run count=$CNT2 status=$ST2 (erwartet 1/live)"

echo
say "Beweis: $PASS/$TOTAL PASS"
[[ "$PASS" = "$TOTAL" ]]
