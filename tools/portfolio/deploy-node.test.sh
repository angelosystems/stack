#!/usr/bin/env bash
# deploy-node.test.sh — hermetischer Selbsttest fuer deploy-node.sh
# (sa-deploy-stufen W4). Baut ein Wegwerf-Git-Repo mit der node-Beweis-Lane und
# deployt LOKAL (kein --box, kein systemd) in ein tmp-Dest. Beweist:
#   A  SHA-gepinnter Bundle-Deploy: current -> releases/<sha>, gebackene Identitaet
#   B  Zweiter Deploy: prev zeigt auf das vorige Release
#   C  Rollback-Reuse (D12): erneuter Deploy einer schon gebauten SHA baut NICHT
#      neu (built:false) — genau der Pfad, den der Reaktor-Rollback nimmt
#   D  --rollback: current flippt auf prev
#   E  Trading-Wall: Live-Geld-Muster in JEDEM Feld -> exit 64 (Regression)
#   F  --dry-run: exit 0, keine Seiteneffekte
#
# Nutzung: bash deploy-node.test.sh   (Exit 0 = alle gruen)
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$HERE/deploy-node.sh"
NODE_PATH_DIR="${SA_NODE_BIN:-/opt/node24/bin}"
CANARY_SRC="${SA_CANARY_APP:-/root/stayawesomeOS/apps/staging-node-canary}"
PASS=0; FAIL=0
ok()   { printf '  \033[1;32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[1;31mFAIL\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

command -v "$NODE_PATH_DIR/node" >/dev/null 2>&1 || { echo "kein node unter $NODE_PATH_DIR — SA_NODE_BIN setzen"; exit 2; }
[[ -x "$SCRIPT" ]] || { echo "deploy-node.sh nicht ausfuehrbar: $SCRIPT"; exit 2; }
[[ -d "$CANARY_SRC" ]] || { echo "Kanary-App fehlt: $CANARY_SRC"; exit 2; }

WORK="$(mktemp -d /tmp/deploy-node-test.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
REPO="$WORK/repo"; DEST="$WORK/dest"
mkdir -p "$REPO/apps/staging-node-canary"
cp "$CANARY_SRC/package.json" "$CANARY_SRC/server.js" "$CANARY_SRC/build.js" "$REPO/apps/staging-node-canary/"

git -C "$REPO" init -q
git -C "$REPO" config user.email t@t; git -C "$REPO" config user.name t
git -C "$REPO" add -A && git -C "$REPO" commit -qm "canary v1"
SHA1="$(git -C "$REPO" rev-parse HEAD)"
# zweite Version (Marker im server) -> anderer Commit
printf '\n// marker v2\n' >> "$REPO/apps/staging-node-canary/server.js"
git -C "$REPO" commit -aqm "canary v2"
SHA2="$(git -C "$REPO" rev-parse HEAD)"

BUILD='node build.js'
run() { "$SCRIPT" --repo "$REPO" --node-path "$NODE_PATH_DIR" --build-cmd "$BUILD" "$@"; }

echo "== A: SHA-gepinnter Bundle-Deploy (sha1) =="
out="$(run --ref "$SHA1" --service snc-test --src apps/staging-node-canary --dest "$DEST" --json 2>/dev/null)" && rc=0 || rc=$?
if [[ $rc -eq 0 ]]; then ok "deploy exit 0"; else bad "deploy exit $rc"; fi
cur="$(readlink "$DEST/current" 2>/dev/null)"
[[ "$cur" == "$DEST/releases/$SHA1" ]] && ok "current -> releases/sha1" || bad "current=$cur"
short1="$(git -C "$REPO" rev-parse --short "$SHA1")"
got="$("$NODE_PATH_DIR/node" "$DEST/current/server.js" version --json 2>/dev/null)"
echo "$got" | grep -q "\"sha\":\"$short1\"" && ok "gebackene sha=$short1 im Bundle" || bad "sha im Bundle: $got"
echo "$got" | grep -q '"service":"staging-node-canary"' && ok "service-Vertrag ok" || bad "service fehlt: $got"
echo "$out" | grep -q '"built":true' && ok "built:true (frischer Build)" || bad "built-Flag: $out"

echo "== B: zweiter Deploy (sha2) -> prev zeigt auf sha1 =="
run --ref "$SHA2" --service snc-test --src apps/staging-node-canary --dest "$DEST" >/dev/null 2>&1 && ok "deploy sha2 exit 0" || bad "deploy sha2"
[[ "$(readlink "$DEST/current")" == "$DEST/releases/$SHA2" ]] && ok "current -> sha2" || bad "current nicht sha2"
[[ "$(readlink "$DEST/prev")" == "$DEST/releases/$SHA1" ]] && ok "prev -> sha1" || bad "prev=$(readlink "$DEST/prev")"

echo "== C: Rollback-Reuse — erneuter Deploy sha1 baut NICHT neu =="
out="$(run --ref "$SHA1" --service snc-test --src apps/staging-node-canary --dest "$DEST" --json 2>/dev/null)"
echo "$out" | grep -q '"built":false' && ok "built:false (Release wiederverwendet, kein Rebuild)" || bad "erwartete built:false: $out"
[[ "$(readlink "$DEST/current")" == "$DEST/releases/$SHA1" ]] && ok "current zurueck auf sha1 (Reaktor-Rollback-Pfad)" || bad "current nicht sha1"

echo "== D: --rollback flippt current auf prev =="
# aktueller Zustand: current=sha1, prev=sha2 (aus C). --rollback -> current=sha2
run --rollback --service snc-test --dest "$DEST" >/dev/null 2>&1 && ok "--rollback exit 0" || bad "--rollback exit"
[[ "$(readlink "$DEST/current")" == "$DEST/releases/$SHA2" ]] && ok "current -> sha2 nach rollback" || bad "current=$(readlink "$DEST/current")"

echo "== E: Trading-Wall — Live-Geld-Muster je Feld -> exit 64 =="
wall() { # $1=beschreibung, rest=args
    local desc="$1"; shift
    "$SCRIPT" --repo "$REPO" --node-path "$NODE_PATH_DIR" --build-cmd "$BUILD" "$@" >/dev/null 2>&1
    [[ $? -eq 64 ]] && ok "Wall $desc -> exit 64" || bad "Wall $desc NICHT 64"
}
wall "service=quantbot"    --ref "$SHA1" --service quantbot-x --src apps/staging-node-canary --dest "$DEST"
wall "unit=live-trad"      --ref "$SHA1" --service snc --unit live-trad.service --src apps/staging-node-canary --dest "$DEST"
wall "src=strategies"      --ref "$SHA1" --service snc --src apps/strategies --dest "$DEST"
wall "dest=supervisor"     --ref "$SHA1" --service snc --src apps/staging-node-canary --dest /opt/supervisor/x
wall "repo=quantbot"       --ref "$SHA1" --service snc --src apps/staging-node-canary --dest "$DEST" --repo /opt/quantbot
wall "box=dublin"          --ref "$SHA1" --service snc --src apps/staging-node-canary --dest "$DEST" --box root@dublin
# Wall greift auch im --rollback-Pfad (ein Rollback ist ein Restart):
"$SCRIPT" --rollback --service quantbot --dest "$DEST" >/dev/null 2>&1
[[ $? -eq 64 ]] && ok "Wall rollback service=quantbot -> exit 64" || bad "Wall rollback NICHT 64"

echo "== F: --dry-run exit 0, keine Seiteneffekte =="
DEST2="$WORK/dest2"
run --ref "$SHA1" --service snc --src apps/staging-node-canary --dest "$DEST2" --dry-run >/dev/null 2>&1 && ok "--dry-run exit 0" || bad "--dry-run exit"
[[ ! -e "$DEST2" ]] && ok "--dry-run schrieb nichts" || bad "--dry-run legte $DEST2 an"

echo
echo "deploy-node.test.sh: $PASS gruen, $FAIL rot"
[[ $FAIL -eq 0 ]]
