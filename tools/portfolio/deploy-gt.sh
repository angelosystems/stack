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
# Warum --box (sa-deploy-stufen W2, Remote-Verallgemeinerung): das
# Live-Board (stack) und der Reaktor laufen auf derselben Box wie der Build —
# lokaler go-build + lokaler bin-Swap + lokaler restart. Die SA-Staging-Box
# (167.233.82.201) hat KEINEN Go-Toolchain und ist über ssh-Key erreichbar.
# --box <ssh-host> baut daher WEITER LOKAL SHA-gepinnt (identischer Worktree-
# Build), shippt das fertige Binary per scp, swappt es atomar per ssh und
# restartet die Unit remote. Ohne --box bleibt der Pfad byte-identisch lokal
# (Rückwärtskompatibilität; der stack-Reaktor setzt --box nie). Cross-Compile
# unnötig, solange Build-Box und Ziel-Box gleiche GOOS/GOARCH haben
# (werkstatt=staging=linux/amd64); sonst müsste der Build GOOS/GOARCH setzen —
# ponytail: dann --goarch/--goos ergänzen.
#
# Aufruf:
#   deploy-gt.sh --ref <sha> --service <name> --src <relpath> --bin <abs> \
#                [--unit <systemd-unit>] [--repo <path>] [--box <ssh-host>] \
#                [--dry-run] [--json]
#
#   --ref     Commit-SHA (Deploy-Ziel ODER Rollback-Ziel). Pflicht.
#   --service Service-Name (nur Logging + Stamp). Pflicht.
#   --src     Go-Paket-Relpfad im Repo, z.B. tools/portfolio/master-kanban. Pflicht.
#   --bin     Ziel-Binary (absoluter Pfad, atomarer Swap). Bei --box: Pfad auf der
#             Ziel-Box. Pflicht.
#   --unit    systemd-Unit für Restart; leer = kein Restart (cli-Service).
#   --repo    Git-Wurzel (Default /opt/stack).
#   --box     ssh-Ziel (z.B. root@167.233.82.201) — baut lokal, deployt remote.
#             Leer = lokaler In-Place-Deploy (Default, unverändert).
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

REF=""; SERVICE=""; SRC=""; BIN=""; UNIT=""; REPO="/opt/stack"; BOX=""
DRY_RUN=0; JSON=0

die64() { printf '\033[1;31mx\033[0m deploy-gt.sh: %s\n' "$*" >&2; exit 64; }
die70() { printf '\033[1;31mx\033[0m deploy-gt.sh: %s\n' "$*" >&2; exit 70; }
die75() { printf '\033[1;31mx\033[0m deploy-gt.sh: %s\n' "$*" >&2; exit 75; }
say()   { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }

# ssh/scp mit unbeaufsichtigten Defaults (kein Passwort-Prompt, kurze Frist).
# Poka-yoke: BatchMode → hängt nie am Prompt; der Reaktor behandelt jeden
# Nicht-Null-Exit als Rollback-Grund.
rsh()  { ssh -o BatchMode=yes -o ConnectTimeout=10 "$BOX" "$@"; }
rcp()  { scp -q -o BatchMode=yes -o ConnectTimeout=10 "$1" "$BOX:$2"; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --ref)     REF="${2:-}"; shift 2 ;;
        --service) SERVICE="${2:-}"; shift 2 ;;
        --src)     SRC="${2:-}"; shift 2 ;;
        --bin)     BIN="${2:-}"; shift 2 ;;
        --unit)    UNIT="${2:-}"; shift 2 ;;
        --repo)    REPO="${2:-}"; shift 2 ;;
        --box)     BOX="${2:-}"; shift 2 ;;
        --dry-run) DRY_RUN=1; shift ;;
        --json)    JSON=1; shift ;;
        -h|--help) sed -n '2,60p' "$0"; exit 0 ;;
        *) die64 "unbekannte Option: $1 (siehe --help)" ;;
    esac
done

# ── Argument-Prüfung (Poka-yoke, handlungsleitende Fehler) ───────────────────
[[ -n "$REF"     ]] || die64 "--ref <sha> misst — welchen Commit soll ich bauen?"
[[ -n "$SERVICE" ]] || die64 "--service <name> misst."
[[ -n "$SRC"     ]] || die64 "--src <relpfad> misst — wo liegt das Go-Paket im Repo?"
[[ -n "$BIN"     ]] || die64 "--bin <pfad> misst — wohin swappen?"
[[ "${BIN:0:1}" == "/" ]] || die64 "--bin muss ein absoluter Pfad sein, war: $BIN"
# Arbeitsbaum-Repo (/opt/stack) ODER bare Mirror (SA-Staging baut aus dem
# Merger-Bare-Mirror, der KEIN .git-Unterverzeichnis hat) — beide sind gültige
# Build-Quellen; git-worktree add funktioniert auf beiden.
git -C "$REPO" rev-parse --git-dir >/dev/null 2>&1 \
    || die64 "--repo $REPO ist kein git-Repo (weder Arbeitsbaum noch bare)."

# ── Trading-Wall (Mario-Go 2026-07-07/08; solartown-vollbetrieb-prd,
#    Abschnitt SICHERHEITSBEFUND): Die Factory darf Live-Geld-Einheiten
#    NIEMALS bauen, swappen oder restarten — egal was Outbox oder Manifest
#    liefern. Bewusst breite Muster statt Unit-Liste und OHNE Override-Flag:
#    Aufweichen heißt, diese Zeilen sehenden Auges zu editieren. Ergänzt die
#    Repo-Wall (pre-receive auf /opt/quantbot), die nur den Push-Pfad deckt —
#    hier wird der Deploy-/Restart-Pfad dicht gemacht.
#    --box wird MITGEPRÜFT: ein Remote-Deploy Richtung einer Live-Geld-Box darf
#    ebenso wenig durch (die Factory swappt/restartet auch remote keine
#    Live-Geld-Einheit).
for _feld in "$SERVICE" "$UNIT" "$SRC" "$BIN" "$REPO" "$BOX"; do
    case "$_feld" in
        *quantbot*|*supervisor*|*strategies*|*dublin*|*live-trad*)
            die64 "Trading-Wall: '$_feld' matcht ein Live-Geld-Muster (quantbot/supervisor/strategies/dublin/live-trad) — Deploy hart verweigert. Live-Geld-Deploys laufen NIE über die Factory." ;;
    esac
done
git -C "$REPO" cat-file -e "${REF}^{commit}" 2>/dev/null \
    || die64 "--ref $REF ist im Repo $REPO nicht auffindbar — schon gefetcht?"
[[ -d "$REPO/$SRC" ]] || warn "src $REPO/$SRC existiert im Live-Baum nicht (wird im Worktree geprüft)"

SHA_SHORT="$(git -C "$REPO" rev-parse --short "$REF")"
VERSION="$(git -C "$REPO" describe --tags --always "$REF" 2>/dev/null || echo "$SHA_SHORT")"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X main.Version=${VERSION} -X main.Sha=${SHA_SHORT} -X main.BuiltAt=${BUILT_AT}"

say "Deploy $SERVICE @ $SHA_SHORT (version=$VERSION) → ${BOX:+$BOX:}$BIN${UNIT:+ (unit $UNIT)}"

if [[ $DRY_RUN -eq 1 ]]; then
    echo "+ git -C $REPO worktree add --detach <WT> $REF"
    echo "+ (cd <WT>/$SRC && go build -ldflags \"$LDFLAGS\" -o <STAGE> .)"
    if [[ -n "$BOX" ]]; then
        echo "+ ssh $BOX mkdir -p $(dirname "$BIN")"
        echo "+ ssh $BOX cp -f $BIN ${BIN}.prev        (Rollback-Anker, remote)"
        echo "+ scp <STAGE> $BOX:${BIN}.stage          (Ship)"
        echo "+ ssh $BOX mv -f ${BIN}.stage $BIN        (atomarer Swap, remote)"
        [[ -n "$UNIT" ]] && echo "+ ssh $BOX systemctl restart $UNIT" || echo "+ (kein --unit → kein Restart, cli-Service)"
    else
        echo "+ cp -f $BIN ${BIN}.prev   (Rollback-Anker)"
        echo "+ mv <STAGE> $BIN          (atomarer Swap)"
        [[ -n "$UNIT" ]] && echo "+ systemctl restart $UNIT" || echo "+ (kein --unit → kein Restart, cli-Service)"
    fi
    exit 0
fi

# ── Worktree anlegen; Trap räumt ihn IMMER weg (auch bei Build-Miss) ─────────
WTDIR="$(mktemp -d "${TMPDIR:-/tmp}/deploy-gt.XXXXXX")"
WT="$WTDIR/wt"
cleanup() {
    git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1 || true
    rm -rf "$WTDIR" 2>/dev/null || true
}
trap cleanup EXIT
git -C "$REPO" worktree add --detach "$WT" "$REF" >/dev/null 2>&1 \
    || die70 "worktree add auf $REF ging nicht — Baum belegt? (git worktree prune)"

[[ -d "$WT/$SRC" ]] || die70 "src $SRC existiert im Commit $SHA_SHORT nicht."

# STAGE ist das frisch gebaute Binary VOR dem Swap. Lokal: neben dem Ziel-BIN
# (gleiches Dateisystem → atomarer mv). Remote (--box): ein lokaler Temp im
# Wegwerf-Worktree-Dir (BIN ist dann ein Pfad auf der Ziel-Box, nicht lokal
# beschreibbar) — danach per scp geshippt.
if [[ -n "$BOX" ]]; then
    STAGE="$WTDIR/stage-bin"
else
    STAGE="${BIN}.stage.${SHA_SHORT}.$$"
fi
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

# Rollback-Anker + atomarer Swap — lokal ODER remote (--box). Dieselbe
# Reihenfolge (prev sichern → swappen), damit Rollback-Builds identisch greifen.
PREV_BIN=""
if [[ -n "$BOX" ]]; then
    rsh "mkdir -p '$(dirname "$BIN")'" || die70 "Remote mkdir für $(dirname "$BIN") auf $BOX ging nicht."
    if rsh "test -x '$BIN'"; then
        PREV_BIN="${BIN}.prev"
        rsh "cp -f '$BIN' '$PREV_BIN'" || warn "remote prev-Backup nach $PREV_BIN ging nicht (weiter)"
    fi
    REMOTE_STAGE="${BIN}.stage.${SHA_SHORT}.$$"
    say "Ship → $BOX:$BIN (SHA-gepinnt)"
    rcp "$STAGE" "$REMOTE_STAGE" || die70 "scp nach $BOX:$REMOTE_STAGE ging nicht."
    say "Atomarer Swap → $BOX:$BIN"
    rsh "chmod +x '$REMOTE_STAGE' && mv -f '$REMOTE_STAGE' '$BIN'" \
        || die70 "Remote-Swap nach $BIN auf $BOX ging nicht."
else
    if [[ -x "$BIN" ]]; then
        PREV_BIN="${BIN}.prev"
        cp -f "$BIN" "$PREV_BIN" || warn "prev-Backup nach $PREV_BIN ging nicht (weiter)"
    fi
    say "Atomarer Swap → $BIN"
    mv -f "$STAGE" "$BIN" || die70 "Swap nach $BIN ging nicht."
fi

# ── Restart (nur bei --unit; cli-Services haben keinen Prozess) — lokal/remote ─
RESTARTED=false
if [[ -n "$UNIT" ]]; then
    if [[ -n "$BOX" ]]; then
        if rsh "systemctl list-unit-files '$UNIT' >/dev/null 2>&1 && systemctl is-enabled '$UNIT' >/dev/null 2>&1"; then
            say "Restart $UNIT @ $BOX"
            if rsh "systemctl restart '$UNIT'"; then
                RESTARTED=true
            else
                die75 "remote restart $UNIT auf $BOX ging nicht (Swap ist schon passiert → Reaktor rollt zurück)."
            fi
        else
            die75 "Unit $UNIT auf $BOX not-found/off — Deploy unvollständig (Pre-Arm-Abgleich, Reaktor eskaliert)."
        fi
    elif systemctl list-unit-files "$UNIT" &>/dev/null && systemctl is-enabled "$UNIT" &>/dev/null; then
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
    printf '{"ok":true,"service":"%s","ref":"%s","sha":"%s","version":"%s","built_at":"%s","box":"%s","bin":"%s","prev_bin":"%s","restarted":%s}\n' \
        "$SERVICE" "$REF" "$SHA_SHORT" "$VERSION" "$BUILT_AT" "$BOX" "$BIN" "$PREV_BIN" "$RESTARTED"
else
    printf '\033[1;32mok\033[0m deploy-gt.sh: %s @ %s scharf (%sbin %s, prev %s, restart %s)\n' \
        "$SERVICE" "$SHA_SHORT" "${BOX:+box $BOX, }" "$BIN" "${PREV_BIN:-—}" "$RESTARTED"
fi
