#!/usr/bin/env bash
# deploy-gt.sh — SHA-gepinnter In-Place-Deploy (Code-Fabrik Release-Pipeline,
# PRD code-fabrik-release-pipeline WP5 · D12/D14/D16).
#
# Die „Hände" des Deploy-Reaktors: baut GENAU den übergebenen Commit und
# schaltet ihn atomar scharf. Der Reaktor (deploy_reactor_outbox.go) ist der
# „Kopf" — er besitzt das Ledger (Lease/Status, D13), die Smoke-Sonde (D18)
# und den Rollback-Entscheid. Dieses Skript schreibt NICHT ins Ledger und
# sondiert NICHT — Trennung der Eigentümerschaft ist Absicht.
#
# Warum SHA-gepinnt (D12): das alte deploy-gt.sh baute blind HEAD. Ein
# „Rollback auf prev_version" war damit gar nicht möglich — der Rollback-Build
# hätte wieder HEAD gebaut. Hier bindet --ref den BUILD an einen exakten
# Commit, für Vorwärts-Deploy UND Rollback dieselbe Mechanik.
#
# Warum git-worktree statt checkout (D14, Poka-yoke): das laufende
# /opt/stack-Arbeitsverzeichnis wird NIE angefasst — kein `checkout` auf dem
# Live-Baum, kein `reset --hard`. Der Build läuft in einem wegwerfbaren
# Worktree, der am Ende restlos entfernt wird. So kann ein Deploy den Baum,
# aus dem der Reaktor selbst läuft, nicht verändern.
#
# Aufruf:
#   deploy-gt.sh --ref <sha> --service <name> --src <relpath> --bin <abs> \
#                [--unit <systemd-unit>] [--repo <path>] [--dry-run] [--json]
#
#   --ref     Commit-SHA (Deploy-Ziel ODER Rollback-Ziel). Pflicht.
#   --service Service-Name (nur Logging + Stamp). Pflicht.
#   --src     Go-Paket-Relpfad im Repo, z.B. tools/portfolio/master-kanban. Pflicht.
#   --bin     Ziel-Binary (absoluter Pfad, atomarer Swap). Pflicht.
#   --unit    systemd-Unit für Restart; leer = kein Restart (cli-Service).
#   --repo    Git-Wurzel (Default /opt/stack).
#   --dry-run zeigt die Schritte, baut/swappt/restartet nicht.
#   --json    letzte Zeile ist ein Maschinen-Ergebnis (JSON) statt Text.
#
# Exit-Codes:
#   0  gebaut + geswappt (+ ggf. restartet) — der Reaktor smoked jetzt.
#   64 Aufruf-Fehler (fehlendes/kaputtes Argument) — handlungsleitend gemeldet.
#   70 Build- oder Swap-Miss (Live-Stand nach Build-Miss unverändert).
#   75 Restart-Miss / Unit not-found nach Swap (Live-Stand ggf. halbfertig →
#      der Reaktor behandelt jeden Nicht-Null-Exit als Rollback-Grund).
#
# Env (optional, Ponytail-Aufhänger für WP7-Härtung):
#   DEPLOY_BUILD_SLICE  systemd-Slice, in der `go build` ressourcen-isoliert
#                       läuft (D16). Leer = Build im aktuellen Kontext. Ceiling:
#                       ohne Slice teilt der Build sich die Box; WP7 setzt die
#                       gedeckelte deploy-reactor.slice.

set -euo pipefail

REF=""; SERVICE=""; SRC=""; BIN=""; UNIT=""; REPO="/opt/stack"
DRY_RUN=0; JSON=0

die64() { printf '\033[1;31mx\033[0m deploy-gt.sh: %s\n' "$*" >&2; exit 64; }
die70() { printf '\033[1;31mx\033[0m deploy-gt.sh: %s\n' "$*" >&2; exit 70; }
die75() { printf '\033[1;31mx\033[0m deploy-gt.sh: %s\n' "$*" >&2; exit 75; }
say()   { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --ref)     REF="${2:-}"; shift 2 ;;
        --service) SERVICE="${2:-}"; shift 2 ;;
        --src)     SRC="${2:-}"; shift 2 ;;
        --bin)     BIN="${2:-}"; shift 2 ;;
        --unit)    UNIT="${2:-}"; shift 2 ;;
        --repo)    REPO="${2:-}"; shift 2 ;;
        --dry-run) DRY_RUN=1; shift ;;
        --json)    JSON=1; shift ;;
        -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
        *) die64 "unbekannte Option: $1 (siehe --help)" ;;
    esac
done

# ── Argument-Prüfung (Poka-yoke, handlungsleitende Fehler) ───────────────────
[[ -n "$REF"     ]] || die64 "--ref <sha> misst — welchen Commit soll ich bauen?"
[[ -n "$SERVICE" ]] || die64 "--service <name> misst."
[[ -n "$SRC"     ]] || die64 "--src <relpfad> misst — wo liegt das Go-Paket im Repo?"
[[ -n "$BIN"     ]] || die64 "--bin <pfad> misst — wohin swappen?"
[[ "${BIN:0:1}" == "/" ]] || die64 "--bin muss ein absoluter Pfad sein, war: $BIN"
[[ -d "$REPO/.git" ]] || die64 "--repo $REPO ist kein git-Repo."
git -C "$REPO" cat-file -e "${REF}^{commit}" 2>/dev/null \
    || die64 "--ref $REF ist im Repo $REPO nicht auffindbar — schon gefetcht?"
[[ -d "$REPO/$SRC" ]] || warn "src $REPO/$SRC existiert im Live-Baum nicht (wird im Worktree geprüft)"

SHA_SHORT="$(git -C "$REPO" rev-parse --short "$REF")"
VERSION="$(git -C "$REPO" describe --tags --always "$REF" 2>/dev/null || echo "$SHA_SHORT")"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X main.Version=${VERSION} -X main.Sha=${SHA_SHORT} -X main.BuiltAt=${BUILT_AT}"

say "Deploy $SERVICE @ $SHA_SHORT (version=$VERSION) → $BIN${UNIT:+ (unit $UNIT)}"

if [[ $DRY_RUN -eq 1 ]]; then
    echo "+ git -C $REPO worktree add --detach <WT> $REF"
    echo "+ (cd <WT>/$SRC && go build -ldflags \"$LDFLAGS\" -o <STAGE> .)"
    echo "+ cp -f $BIN ${BIN}.prev   (Rollback-Anker)"
    echo "+ mv <STAGE> $BIN          (atomarer Swap)"
    [[ -n "$UNIT" ]] && echo "+ systemctl restart $UNIT" || echo "+ (kein --unit → kein Restart, cli-Service)"
    exit 0
fi

# ── Worktree anlegen; Trap räumt ihn IMMER weg (auch bei Build-Miss) ─────────
WT="$(mktemp -d "${TMPDIR:-/tmp}/deploy-gt.XXXXXX")/wt"
cleanup() {
    git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1 || true
    rmdir "$(dirname "$WT")" 2>/dev/null || true
}
trap cleanup EXIT
git -C "$REPO" worktree add --detach "$WT" "$REF" >/dev/null 2>&1 \
    || die70 "worktree add auf $REF ging nicht — Baum belegt? (git worktree prune)"

[[ -d "$WT/$SRC" ]] || die70 "src $SRC existiert im Commit $SHA_SHORT nicht."

STAGE="${BIN}.stage.${SHA_SHORT}.$$"
say "Baue aus Worktree (SHA-gepinnt, Live-Baum unberührt)"
BUILD=(go build -ldflags "$LDFLAGS" -o "$STAGE" .)
if [[ -n "${DEPLOY_BUILD_SLICE:-}" ]]; then
    # D16: Build ressourcen-isoliert. Ponytail-Ceiling: nur wenn die Slice
    # existiert; WP7 legt die gedeckelte deploy-reactor.slice an.
    BUILD=(systemd-run --scope --quiet --slice="$DEPLOY_BUILD_SLICE" "${BUILD[@]}")
fi
if ! ( cd "$WT/$SRC" && "${BUILD[@]}" ); then
    rm -f "$STAGE"
    die70 "go build @ $SHA_SHORT ging nicht — Live-Stand unverändert (kein Swap)."
fi
[[ -x "$STAGE" ]] || die70 "Build lieferte kein Binary unter $STAGE."

# Rollback-Anker: den aktuellen Stand sichern, bevor wir swappen.
PREV_BIN=""
if [[ -x "$BIN" ]]; then
    PREV_BIN="${BIN}.prev"
    cp -f "$BIN" "$PREV_BIN" || warn "prev-Backup nach $PREV_BIN ging nicht (weiter)"
fi

say "Atomarer Swap → $BIN"
mv -f "$STAGE" "$BIN" || die70 "Swap nach $BIN ging nicht."

# ── Restart (nur bei --unit; cli-Services haben keinen Prozess) ──────────────
RESTARTED=false
if [[ -n "$UNIT" ]]; then
    if systemctl list-unit-files "$UNIT" &>/dev/null && systemctl is-enabled "$UNIT" &>/dev/null; then
        say "Restart $UNIT"
        if systemctl restart "$UNIT"; then
            RESTARTED=true
        else
            die75 "systemctl restart $UNIT ging nicht (Swap ist schon passiert → Reaktor rollt zurück)."
        fi
    else
        die75 "Unit $UNIT not-found/off — Deploy unvollständig (Pre-Arm-Abgleich, Reaktor eskaliert)."
    fi
fi

if [[ $JSON -eq 1 ]]; then
    printf '{"ok":true,"service":"%s","ref":"%s","sha":"%s","version":"%s","built_at":"%s","bin":"%s","prev_bin":"%s","restarted":%s}\n' \
        "$SERVICE" "$REF" "$SHA_SHORT" "$VERSION" "$BUILT_AT" "$BIN" "$PREV_BIN" "$RESTARTED"
else
    printf '\033[1;32mok\033[0m deploy-gt.sh: %s @ %s scharf (bin %s, prev %s, restart %s)\n' \
        "$SERVICE" "$SHA_SHORT" "$BIN" "${PREV_BIN:-—}" "$RESTARTED"
fi
