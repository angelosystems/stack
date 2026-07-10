#!/usr/bin/env bash
# deploy-node.sh — SHA-gepinnter Bundle-Deploy für Node/Next.js-Apps
# (sa-deploy-stufen W4 · Schwester zu deploy-gt.sh).
#
# deploy-gt.sh swappt EIN Go-Binary; die realen SA-Apps (sa-fin = Next.js) sind
# aber ein VERZEICHNIS: gebautes `.next` + node_modules + package.json + public.
# Diese Datei ist die node-Variante mit derselben Disziplin und denselben
# Eigentümerschafts-Grenzen: sie baut GENAU den übergebenen Commit und schaltet
# ihn atomar scharf. Der Reaktor (deploy_reactor_outbox.go) bleibt der „Kopf" —
# er besitzt das Ledger, sondiert (Smoke) und entscheidet Rollback. Dieses
# Skript schreibt NICHT ins Ledger und sondiert NICHT.
#
# SHA-gepinnt (D12): --ref bindet den BUILD an einen exakten Commit; identisch
# für Vorwärts-Deploy und Rollback. git-worktree statt checkout (D14, Poka-yoke):
# der Live-Baum (/opt/stack ODER der stayawesomeOS-Bare-Mirror) wird NIE
# angefasst — Build in einem Wegwerf-Worktree.
#
# Atomarer Swap OHNE Binary: ein Bundle-Verzeichnis wird per rsync nach
#   <dest>/releases/<sha>/ geshippt, dann zeigt der Symlink <dest>/current
# atomar (rename(2)) auf das neue Release. Die App-Unit hat WorkingDirectory=
# <dest>/current — ein Restart lädt aus dem neuen Ziel. <dest>/prev zeigt auf das
# vorige Release (Rollback-Anker).
#
# ROLLBACK ohne Rebuild (der wichtige Unterschied zu Go): ein per-SHA-Release
# bleibt auf der Box liegen (Marker .deploy-ok). Ruft der Reaktor deploy-node.sh
# ERNEUT mit der prev-SHA (sein SHA-gepinnter Rollback-Pfad), ist deren Release
# schon da → NUR Symlink-Flip + Restart, KEIN teurer node-Rebuild. Zusätzlich
# gibt es --rollback (flip current↔prev) für den promote-Pfad.
#
# Aufruf:
#   deploy-node.sh --ref <sha> --service <name> --src <relpath> --dest <abs-dir> \
#                  [--unit <systemd-unit>] [--repo <path>] [--box <ssh-host>] \
#                  [--build-cmd "<cmd>"] [--node-path <dir>] [--keep <N>] \
#                  [--force-build] [--dry-run] [--json]
#   deploy-node.sh --rollback --service <name> --dest <abs-dir> \
#                  [--unit <unit>] [--box <ssh-host>] [--json]
#
#   --ref       Commit-SHA (Deploy- ODER Rollback-Ziel). Pflicht (außer --rollback).
#   --service   Service-Name (Logging + Wall). Pflicht.
#   --src       App-Relpfad im Repo, z.B. apps/fin. Pflicht (außer --rollback).
#   --dest      Deploy-Wurzel auf der Ziel-Box (absoluter Pfad). Enthält
#               releases/, current, prev. Pflicht.
#   --unit      systemd-Unit für Restart; leer = kein Restart.
#   --repo      Git-Wurzel (Default /opt/stack; SA: der Bare-Mirror).
#   --box       ssh-Ziel (baut lokal, shippt/flippt/restartet remote). Leer=lokal.
#   --build-cmd Build im App-Dir. Default "pnpm install --frozen-lockfile && pnpm run build".
#   --node-path Verzeichnis mit node/pnpm (Build-PATH-Prefix). Default /opt/node24/bin.
#   --keep      Aufbewahrte Releases (Rest wird gepruned). Default 5.
#   --force-build  auch bauen, wenn das Release schon existiert (Marker ignorieren).
#   --rollback  Kein Build/Ship — flippt <dest>/current auf <dest>/prev + Restart.
#   --dry-run   zeigt die Schritte, baut/shippt/flippt nicht.
#   --json      letzte Zeile = Maschinen-Ergebnis (JSON).
#
# Exit-Codes (deckungsgleich mit deploy-gt.sh):
#   0  gebaut/geflippt (+ ggf. restartet) — der Reaktor smoked jetzt.
#   64 Aufruf-/Wall-Fehler (handlungsleitend gemeldet).
#   70 Build-/Ship-/Flip-Miss (Live-Stand nach Build-Miss unverändert).
#   75 Restart-Miss / Unit not-found nach Flip (Reaktor rollt zurück).

set -euo pipefail

REF=""; SERVICE=""; SRC=""; DEST=""; UNIT=""; REPO="/opt/stack"; BOX=""
BUILD_CMD="pnpm install --frozen-lockfile && pnpm run build"
NODE_PATH_DIR="/opt/node24/bin"; KEEP=5
FORCE_BUILD=0; ROLLBACK=0; DRY_RUN=0; JSON=0

die64() { printf '\033[1;31mx\033[0m deploy-node.sh: %s\n' "$*" >&2; exit 64; }
die70() { printf '\033[1;31mx\033[0m deploy-node.sh: %s\n' "$*" >&2; exit 70; }
die75() { printf '\033[1;31mx\033[0m deploy-node.sh: %s\n' "$*" >&2; exit 75; }
say()   { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10)
# on_target führt einen Shell-Befehl lokal ODER auf der Box aus — identischer
# Skript-Text, ein Pfad. Poka-yoke: BatchMode → hängt nie am Prompt.
on_target() { if [[ -n "$BOX" ]]; then ssh "${SSH_OPTS[@]}" "$BOX" "$1"; else bash -c "$1"; fi; }
# ship_dir rsync't ein fertiges Bundle-Dir nach <ziel> (lokal oder box:pfad).
ship_dir() {
    if [[ -n "$BOX" ]]; then
        rsync -a --delete -e "ssh ${SSH_OPTS[*]}" "$1/" "$BOX:$2/"
    else
        rsync -a --delete "$1/" "$2/"
    fi
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --ref)        REF="${2:-}"; shift 2 ;;
        --service)    SERVICE="${2:-}"; shift 2 ;;
        --src)        SRC="${2:-}"; shift 2 ;;
        --dest)       DEST="${2:-}"; shift 2 ;;
        --unit)       UNIT="${2:-}"; shift 2 ;;
        --repo)       REPO="${2:-}"; shift 2 ;;
        --box)        BOX="${2:-}"; shift 2 ;;
        --build-cmd)  BUILD_CMD="${2:-}"; shift 2 ;;
        --node-path)  NODE_PATH_DIR="${2:-}"; shift 2 ;;
        --keep)       KEEP="${2:-}"; shift 2 ;;
        --force-build) FORCE_BUILD=1; shift ;;
        --rollback)   ROLLBACK=1; shift ;;
        --dry-run)    DRY_RUN=1; shift ;;
        --json)       JSON=1; shift ;;
        -h|--help)    sed -n '2,60p' "$0"; exit 0 ;;
        *) die64 "unbekannte Option: $1 (siehe --help)" ;;
    esac
done

# ── Argument-Prüfung (Poka-yoke) ─────────────────────────────────────────────
[[ -n "$SERVICE" ]] || die64 "--service <name> misst."
[[ -n "$DEST"    ]] || die64 "--dest <dir> misst — wohin deployen?"
[[ "${DEST:0:1}" == "/" ]] || die64 "--dest muss ein absoluter Pfad sein, war: $DEST"
if [[ $ROLLBACK -eq 0 ]]; then
    [[ -n "$REF" ]] || die64 "--ref <sha> misst — welchen Commit soll ich bauen?"
    [[ -n "$SRC" ]] || die64 "--src <relpfad> misst — wo liegt die App im Repo?"
fi

# ── Trading-Wall (Mario-Go 2026-07-07/08) — identisch zu deploy-gt.sh: die
#    Factory darf Live-Geld-Einheiten NIEMALS bauen/shippen/restarten. Breite
#    Muster statt Unit-Liste, KEIN Override. --box wird mitgeprüft (kein
#    Remote-Deploy Richtung einer Live-Geld-Box). Auch der --rollback-Pfad läuft
#    hier durch — ein Rollback IST ein Restart auf der Box.
for _feld in "$SERVICE" "$UNIT" "$SRC" "$DEST" "$REPO" "$BOX"; do
    case "$_feld" in
        *quantbot*|*supervisor*|*strategies*|*dublin*|*live-trad*)
            die64 "Trading-Wall: '$_feld' matcht ein Live-Geld-Muster (quantbot/supervisor/strategies/dublin/live-trad) — Deploy hart verweigert. Live-Geld-Deploys laufen NIE über die Factory." ;;
    esac
done

# ── Restart-Helfer (lokal/remote, Pre-Arm-Abgleich wie deploy-gt.sh) ─────────
restart_unit() {
    [[ -n "$UNIT" ]] || return 0
    if on_target "systemctl list-unit-files '$UNIT' >/dev/null 2>&1 && systemctl is-enabled '$UNIT' >/dev/null 2>&1"; then
        say "Restart $UNIT${BOX:+ @ $BOX}"
        on_target "systemctl restart '$UNIT'" || die75 "restart $UNIT ging nicht (Swap ist schon passiert → Reaktor rollt zurück)."
        RESTARTED=true
    else
        die75 "Unit $UNIT${BOX:+ auf $BOX} not-found/off — Deploy unvollständig (Pre-Arm-Abgleich, Reaktor eskaliert)."
    fi
}

RESTARTED=false

# ── Rollback-Pfad: flip current↔prev, KEIN Build ─────────────────────────────
if [[ $ROLLBACK -eq 1 ]]; then
    say "Rollback $SERVICE: flip ${BOX:+$BOX:}$DEST/current → prev"
    if [[ $DRY_RUN -eq 1 ]]; then
        echo "+ on_target: prev=\$(readlink $DEST/prev); cur=\$(readlink $DEST/current); swap current/prev; restart $UNIT"
        exit 0
    fi
    on_target "
        set -e
        prevt=\$(readlink '$DEST/prev' 2>/dev/null || true)
        [ -n \"\$prevt\" ] || { echo 'kein $DEST/prev — nichts zum Zurückflippen' >&2; exit 70; }
        curt=\$(readlink '$DEST/current' 2>/dev/null || true)
        ln -sfn \"\$curt\" '$DEST/prev.tmp' && mv -Tf '$DEST/prev.tmp' '$DEST/prev'
        ln -sfn \"\$prevt\" '$DEST/current.tmp' && mv -Tf '$DEST/current.tmp' '$DEST/current'
    " || die70 "Rollback-Flip auf $DEST ging nicht."
    restart_unit
    if [[ $JSON -eq 1 ]]; then
        printf '{"ok":true,"service":"%s","rollback":true,"box":"%s","dest":"%s","restarted":%s}\n' "$SERVICE" "$BOX" "$DEST" "$RESTARTED"
    else
        printf '\033[1;32mok\033[0m deploy-node.sh: %s auf prev zurückgeflippt (%srestart %s)\n' "$SERVICE" "${BOX:+box $BOX, }" "$RESTARTED"
    fi
    exit 0
fi

# ── Deploy-Pfad ──────────────────────────────────────────────────────────────
git -C "$REPO" rev-parse --git-dir >/dev/null 2>&1 \
    || die64 "--repo $REPO ist kein git-Repo (weder Arbeitsbaum noch bare)."
git -C "$REPO" cat-file -e "${REF}^{commit}" 2>/dev/null \
    || die64 "--ref $REF ist im Repo $REPO nicht auffindbar — schon gefetcht?"

SHA_SHORT="$(git -C "$REPO" rev-parse --short "$REF")"
SHA_FULL="$(git -C "$REPO" rev-parse "$REF")"
VERSION="$(git -C "$REPO" describe --tags --always "$REF" 2>/dev/null || echo "$SHA_SHORT")"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
RELDIR="$DEST/releases/$SHA_FULL"

say "Deploy $SERVICE @ $SHA_SHORT (version=$VERSION) → ${BOX:+$BOX:}$DEST/current${UNIT:+ (unit $UNIT)}"

if [[ $DRY_RUN -eq 1 ]]; then
    echo "+ git -C $REPO worktree add --detach <WT> $REF"
    echo "+ (falls $RELDIR/.deploy-ok fehlt): cd <WT>/$SRC && PATH=$NODE_PATH_DIR:\$PATH SA_BUILD_SHA=$SHA_SHORT $BUILD_CMD"
    echo "+ rsync <WT>/$SRC/ → ${BOX:+$BOX:}$RELDIR/   (+ touch .deploy-ok)"
    echo "+ on_target: prev=readlink current; ln -sfn $RELDIR $DEST/current (atomar)"
    [[ -n "$UNIT" ]] && echo "+ on_target: systemctl restart $UNIT" || echo "+ (kein --unit → kein Restart)"
    echo "+ prune: releases behalten=$KEEP"
    exit 0
fi

# Release schon vollständig da? → Build+Ship überspringen (Rollback-Reuse, D12).
BUILT=false
if [[ $FORCE_BUILD -eq 0 ]] && on_target "test -f '$RELDIR/.deploy-ok'"; then
    say "Release $SHA_SHORT existiert bereits (Marker .deploy-ok) → nur Flip (kein Rebuild)"
else
    # Worktree anlegen; Trap räumt ihn IMMER weg (auch bei Build-Miss).
    WTDIR="$(mktemp -d "${TMPDIR:-/tmp}/deploy-node.XXXXXX")"
    WT="$WTDIR/wt"
    cleanup() {
        git -C "$REPO" worktree remove --force "$WT" >/dev/null 2>&1 || true
        rm -rf "$WTDIR" 2>/dev/null || true
    }
    trap cleanup EXIT
    git -C "$REPO" worktree add --detach "$WT" "$REF" >/dev/null 2>&1 \
        || die70 "worktree add auf $REF ging nicht — Baum belegt? (git worktree prune)"
    [[ -d "$WT/$SRC" ]] || die70 "src $SRC existiert im Commit $SHA_SHORT nicht."

    say "Baue Bundle aus Worktree (SHA-gepinnt, Live-Baum unberührt): $BUILD_CMD"
    if ! ( cd "$WT/$SRC" \
            && export PATH="$NODE_PATH_DIR:$PATH" \
            && export SA_BUILD_SHA="$SHA_SHORT" SA_BUILD_VERSION="$VERSION" SA_BUILD_AT="$BUILT_AT" \
            && eval "$BUILD_CMD" ); then
        die70 "Build @ $SHA_SHORT ging nicht — Live-Stand unverändert (kein Flip)."
    fi

    say "Ship → ${BOX:+$BOX:}$RELDIR (rsync)"
    on_target "mkdir -p '$DEST/releases'" || die70 "mkdir $DEST/releases${BOX:+ auf $BOX} ging nicht."
    ship_dir "$WT/$SRC" "$RELDIR" || die70 "rsync nach $RELDIR ging nicht."
    on_target "touch '$RELDIR/.deploy-ok'" || die70 "Release-Marker in $RELDIR setzen ging nicht."
    BUILT=true
fi

# ── Atomarer Symlink-Flip: prev = altes current, current = neues Release ──────
say "Atomarer Flip → ${BOX:+$BOX:}$DEST/current"
on_target "
    set -e
    oldt=\$(readlink '$DEST/current' 2>/dev/null || true)
    if [ -n \"\$oldt\" ] && [ \"\$oldt\" != '$RELDIR' ]; then
        ln -sfn \"\$oldt\" '$DEST/prev.tmp' && mv -Tf '$DEST/prev.tmp' '$DEST/prev'
    fi
    ln -sfn '$RELDIR' '$DEST/current.tmp' && mv -Tf '$DEST/current.tmp' '$DEST/current'
" || die70 "Symlink-Flip auf $DEST/current ging nicht."

restart_unit

# ── Prune alte Releases (behalte KEEP + das aktuelle/prev-Ziel) ───────────────
on_target "
    cd '$DEST/releases' 2>/dev/null || exit 0
    keep_cur=\$(basename \"\$(readlink '$DEST/current' 2>/dev/null || true)\")
    keep_prev=\$(basename \"\$(readlink '$DEST/prev' 2>/dev/null || true)\")
    ls -1dt */ 2>/dev/null | sed 's#/\$##' | tail -n +$((KEEP+1)) | while read -r d; do
        [ \"\$d\" = \"\$keep_cur\" ] && continue
        [ \"\$d\" = \"\$keep_prev\" ] && continue
        rm -rf -- \"\$d\"
    done
" || warn "Prune alter Releases in $DEST/releases ging nicht (nicht fatal)."

if [[ $JSON -eq 1 ]]; then
    printf '{"ok":true,"service":"%s","ref":"%s","sha":"%s","version":"%s","built_at":"%s","built":%s,"box":"%s","dest":"%s","release":"%s","restarted":%s}\n' \
        "$SERVICE" "$REF" "$SHA_SHORT" "$VERSION" "$BUILT_AT" "$BUILT" "$BOX" "$DEST" "$RELDIR" "$RESTARTED"
else
    printf '\033[1;32mok\033[0m deploy-node.sh: %s @ %s scharf (%scurrent→%s, built %s, restart %s)\n' \
        "$SERVICE" "$SHA_SHORT" "${BOX:+box $BOX, }" "$RELDIR" "$BUILT" "$RESTARTED"
fi
