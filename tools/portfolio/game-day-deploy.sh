#!/usr/bin/env bash
# game-day-deploy.sh — WP6-Rollback-Übung: der Deploy-Reaktor übt seinen
# GEFÄHRLICHSTEN Pfad echt, nicht erst im Brand (spiegelt Test-Gate-PRD M4:
# „teste den Deploy-Reaktor selbst"). Deterministisch, isoliert, aufräumend.
#
# Übt die vier Rollback-Übergänge (D12/D13/D15/D20c) + den Circuit-Breaker:
#   1. rot → deploy-gt.sh --ref <prev>  (SHA-gepinnt zurückgebaut, real)
#   2. →   rolled_back                  (Status terminal, Gift-SHA quarantänisiert)
#   3. →   Eskalation                   (durables MR_DEPLOY_ROLLED_BACK-Artefakt)
#   4. Circuit-Breaker                  (K rote in Folge → Riegel, Rest übersprungen)
#
# Opfer (Opfer-Service): ein winziger, in <1s baubarer Go-Service in einem
# Scratch-git-Repo. Rot wird via SMOKE_FORCE_RED=1 deterministisch injiziert
# (nicht durch ein wirklich kaputtes Artefakt — der Reaktor-Pfad ist derselbe).
#
# Aufruf:  game-day-deploy.sh          (übt, räumt auf)
#          KEEP=1 game-day-deploy.sh   (Scratch stehen lassen)

set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

MK_SRC_ABS="/opt/stack/tools/portfolio/master-kanban"
DEPLOY_GT="/opt/stack/tools/portfolio/deploy-gt.sh"
SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/game-day.XXXXXX")"
SCRATCH_DB="deploy_gameday_d"
GREPO="$SCRATCH/opfer-repo"
VBIN="$SCRATCH/opfersvc"
PASS=0; TOTAL=0
say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { TOTAL=$((TOTAL+1)); PASS=$((PASS+1)); printf '\033[1;32m  ok\033[0m %s\n' "$*"; }
miss() { TOTAL=$((TOTAL+1)); printf '\033[1;31m  MISS\033[0m %s\n' "$*"; }

BASE_DSN="$(sed -n 's/.*envOr("PORTFOLIO_DSN", "\([^"]*\)".*/\1/p' "$MK_SRC_ABS/main.go" | head -1)"
[[ -n "$BASE_DSN" ]] || { echo "DSN nicht auffindbar" >&2; exit 1; }
ADMIN_DSN="$BASE_DSN"
SCRATCH_DSN="$(echo "$BASE_DSN" | sed -E "s#(127.0.0.1:[0-9]+/)[^?]+#\1${SCRATCH_DB}#")"
q() { psql "$SCRATCH_DSN" -Atqc "$1"; }

cleanup() {
    [[ "${KEEP:-0}" = "1" ]] && { say "KEEP=1 → Scratch bleibt: DB=$SCRATCH_DB DIR=$SCRATCH"; return; }
    psql "$ADMIN_DSN" -Atqc "DROP DATABASE IF EXISTS $SCRATCH_DB" >/dev/null 2>&1 || true
    git -C "$GREPO" worktree prune >/dev/null 2>&1 || true
    rm -rf "$SCRATCH"
}
trap cleanup EXIT

# ── Reaktor-Binary (WP5-Code) + Opfer-Repo mit prev- und Gift-Commit ─────────
say "Reaktor-Binary aus dem Arbeitsbaum bauen"
( cd "$MK_SRC_ABS" && go build -o "$SCRATCH/mk" . )

say "Opfer-Repo mit winzigem Service anlegen (prev + Gift-Commit)"
mkdir -p "$GREPO"; cd "$GREPO"
git init -q; git config user.name T; git config user.email t@t; git config commit.gpgsign false
cat > go.mod <<'EOF'
module opfersvc

go 1.21
EOF
write_prog() { cat > main.go <<EOF
package main

import (
	"fmt"
	"os"
)

var Sha string
var Version string

// $1 markiert die Quelle (prev vs. Gift) — beweist SHA-gepinntes Zurückbauen.
func main() {
	if len(os.Args) >= 3 && os.Args[1] == "version" && os.Args[2] == "--json" {
		fmt.Printf("{\"service\":\"opfersvc\",\"version\":\"%s\",\"sha\":\"%s\",\"built_at\":\"\",\"env\":\"prod-mvp\"}\n", Version, Sha)
		return
	}
	fmt.Println("opfersvc quelle=$1 sha=" + Sha)
}
EOF
}
write_prog "prev"; git add -A; git commit -qm "prev"
SHA_PREV="$(git rev-parse HEAD)"; SHORT_PREV="$(git rev-parse --short HEAD)"
write_prog "gift"; git add -A; git commit -qm "gift"
SHA_GIFT="$(git rev-parse HEAD)"; SHORT_GIFT="$(git rev-parse --short HEAD)"
cd "$SCRATCH"

# ── Scratch-Ledger ───────────────────────────────────────────────────────────
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

cat > "$SCRATCH/manifest.yaml" <<YAML
repo: $GREPO
services:
  opfersvc:
    src: .
    bin: $VBIN
    unit: ""
    probe_kind: cli
    health_url: $VBIN
YAML

reactor() { # $1 = SMOKE_FORCE_RED (leer/1)
    SMOKE_FORCE_RED="${1:-}" "$SCRATCH/mk" deploy-reactor-outbox --once \
        --dsn "$SCRATCH_DSN" --manifest "$SCRATCH/manifest.yaml" --script "$DEPLOY_GT" \
        --state-dir "$SCRATCH/state" --events-dir "$SCRATCH/events" \
        --smoke-sleep 100ms --max-smoke 3 --breaker-k 3
}
pending() { q "INSERT INTO portfolio.deployments (service,environment,git_sha,status) VALUES ('opfersvc','prod-mvp','$1','pending')" >/dev/null; }

# ── Phase 1: prev grün etablieren ────────────────────────────────────────────
say "Phase 1 — prev-Stand ($SHORT_PREV) grün deployen"
pending "$SHA_PREV"; reactor
[[ "$(q "SELECT status FROM portfolio.deployments WHERE git_sha='$SHA_PREV'")" = "live" ]] \
    && ok "prev live" || miss "prev nicht live"

# ── Phase 2: Gift-Commit rot → Rollback auf prev ─────────────────────────────
say "Phase 2 — Gift-Commit ($SHORT_GIFT) mit SMOKE_FORCE_RED → Rollback"
pending "$SHA_GIFT"; reactor 1
GST="$(q "SELECT status FROM portfolio.deployments WHERE git_sha='$SHA_GIFT'")"
[[ "$GST" = "rolled_back" ]] && ok "Übergang 2: Gift-Zeile → rolled_back" || miss "Gift-Status=$GST (erwartet rolled_back)"

VER_SHA="$("$VBIN" version --json 2>/dev/null | sed -n 's/.*"sha":"\([^"]*\)".*/\1/p')"
[[ "$VER_SHA" = "$SHORT_PREV" ]] && ok "Übergang 1: Binary SHA-gepinnt auf prev zurückgebaut ($VER_SHA)" \
    || miss "Binary-sha=$VER_SHA erwartet prev $SHORT_PREV"

ART="$(ls "$SCRATCH/events"/MR_DEPLOY_ROLLED_BACK-*.json 2>/dev/null | head -1 || true)"
[[ -n "$ART" ]] && grep -q '"quarantined": true' "$ART" \
    && ok "Übergang 3: durables Eskalations-Artefakt + Quarantäne-Marker" || miss "kein Rollback-Artefakt"

# Quarantäne: dieselbe Gift-SHA lässt sich nicht erneut als pending einschleusen.
if q "INSERT INTO portfolio.deployments (service,environment,git_sha,status) VALUES ('opfersvc','prod-mvp','$SHA_GIFT','pending')" >/dev/null 2>&1; then
    miss "Gift-SHA war NICHT quarantänisiert (Doppel-Insert ging durch)"
else
    ok "Quarantäne: Gift-SHA bleibt rolled_back, kein Re-Deploy (D15)"
fi

# ── Phase 3: Circuit-Breaker (K rote in Folge) ───────────────────────────────
say "Phase 3 — drei rote Deploys in Folge → Circuit-Breaker öffnet"
# frische Gift-SHAs erzeugen (leere Commits), damit jede eine eigene Zeile hat
cd "$GREPO"; B=()
for i in 1 2 3 4; do git commit -q --allow-empty -m "rot$i"; B+=("$(git rev-parse HEAD)"); done
cd "$SCRATCH"
rm -f "$SCRATCH/state/DEPLOY_BREAKER_OPEN"
for s in "${B[@]}"; do pending "$s"; done
reactor 1
BREAK="$SCRATCH/state/DEPLOY_BREAKER_OPEN"
[[ -f "$BREAK" ]] && ok "Circuit-Breaker OFFEN nach 3 roten (Marker liegt)" || miss "Breaker öffnete nicht"
STILL_PENDING="$(q "SELECT count(*) FROM portfolio.deployments WHERE git_sha='${B[3]}' AND status='pending'")"
[[ "$STILL_PENDING" = "1" ]] && ok "4. Zeile unberührt (Drain nach Riegel gestoppt)" || miss "4. Zeile nicht mehr pending"
BREAK_ART="$(ls "$SCRATCH/events"/GATE_BREAKER_OPEN-*.json 2>/dev/null | head -1 || true)"
[[ -n "$BREAK_ART" ]] && ok "GATE_BREAKER_OPEN eskaliert (durables Artefakt)" || miss "kein Breaker-Artefakt"

echo
say "Game-Day: $PASS/$TOTAL PASS"
[[ "$PASS" = "$TOTAL" ]]
