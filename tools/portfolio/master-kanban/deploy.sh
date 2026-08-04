#!/bin/bash
# Build + Deploy master-kanban. Kanonischer Build-Weg (Release-Pipeline-PRD WP2):
# stampt version/sha/built_at via -ldflags, damit /api/version + `version --json`
# den echten Stand liefern statt "dev".
#
# Aufruf:
#   deploy.sh [<sha>]                 → build + atomic swap + restart (normaler Deploy)
#   BUILD_ONLY=1 OUT=<pfad> deploy.sh → nur bauen (Stage-Binary), KEIN swap/restart
#
# <sha> = Deploy-Ziel-Commit (vom Deploy-Reaktor gesetzt); Default: aktueller HEAD.
set -e

BINARY_NAME="master-kanban"
SERVICE_NAME="master-kanban-serve"
SRC_DIR="/opt/stack/tools/portfolio/master-kanban"
REPO_DIR="/opt/stack"

# SHA des Deploy-Ziels. Fallback auf HEAD für manuelle Builds.
SHA="${1:-$(git -C "${REPO_DIR}" rev-parse --short HEAD)}"
# version = git describe: semver-Tag wenn vorhanden, sonst Kurz-SHA (--always).
# Kein Tag heute → version == Kurz-SHA (SHA-Fallback, wie im PRD/Auftrag verlangt).
VERSION="$(git -C "${REPO_DIR}" describe --tags --always 2>/dev/null || echo "${SHA}")"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Build-Provenance (mk-vorhaben-ebene WP-0): ${SHA} ist die Mirror-SHA aus
# stack.git und existiert in master-kanban.git nicht — von der laufenden
# Instanz liess sich der Quell-Commit bisher nicht bestimmen. mirror-to-stack.sh
# haengt einen "Source-SHA:"-Trailer an den Mirror-Commit; den lesen wir hier
# und stampen ihn mit. Fehlt der Trailer (handgemachter Alt-Mirror), bleibt
# source_sha ehrlich "unknown" statt zu raten.
SOURCE_SHA="$(git -C "${REPO_DIR}" log -1 --format=%B "${SHA}" 2>/dev/null \
  | sed -n 's/^Source-SHA:[[:space:]]*//p' | head -1)"
SOURCE_SHA="${SOURCE_SHA:-unknown}"

LDFLAGS="-X main.Version=${VERSION} -X main.Sha=${SHA} -X main.BuiltAt=${BUILT_AT} -X main.SourceSha=${SOURCE_SHA}"

OUT="${OUT:-/opt/stack/bin/${BINARY_NAME}.${SHA}}"

echo "Building ${BINARY_NAME}: version=${VERSION} sha=${SHA} source_sha=${SOURCE_SHA} built_at=${BUILT_AT}"
cd "${SRC_DIR}"
go build -ldflags "${LDFLAGS}" -o "${OUT}" .

if [ "${BUILD_ONLY:-0}" = "1" ]; then
  echo "BUILD_ONLY=1 → Stage-Binary liegt unter ${OUT} (kein swap, kein restart)."
  exit 0
fi

# Release-Ledger-Zeile bei Deploy-START (Release-Pipeline-PRD WP3): Zeile wird
# 'deploying' geschrieben, die 60-s-Probe (Reconciler) bestätigt nur noch →
# 'live'. Idempotenz/Quarantäne-Semantik lebt in ledger-record.sh; kein
# Ledger-Eintrag ⇒ kein Deploy (set -e). Not-Ausstieg: LEDGER_SKIP=1.
HEALTH_URL="http://127.0.0.1:7780/api/version"
DEPLOY_PREV_VERSION="${DEPLOY_PREV_VERSION:-$(curl -s -m 2 "${HEALTH_URL}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("version",""))' 2>/dev/null || true)}" \
DEPLOY_METHOD="${DEPLOY_METHOD:-mk-deploy-sh}" \
  "${REPO_DIR}/tools/portfolio/ledger-record.sh" "${BINARY_NAME}" http "${VERSION}" "${SHA}" "${HEALTH_URL}"

echo "Atomic swap of binary to /opt/stack/bin/${BINARY_NAME}"
mv "${OUT}" "/opt/stack/bin/${BINARY_NAME}"

echo "Creating symlink /usr/local/bin/mk -> /opt/stack/bin/${BINARY_NAME}"
ln -sf "/opt/stack/bin/${BINARY_NAME}" /usr/local/bin/mk

echo "Restarting service ${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"

echo "Deploy of ${BINARY_NAME} completed successfully."
