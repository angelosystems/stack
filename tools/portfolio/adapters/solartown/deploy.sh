#!/bin/bash
# Build + Deploy solartown-adapter. Kanonischer Build-Weg (Release-Pipeline WP2):
# stampt version/sha/built_at via -ldflags — `solartown-adapter version --json`
# liefert den echten Stand (CLI-Oberfläche des /version-Vertrags, D18) —
# und schreibt die Release-Ledger-Zeile (WP3, ledger-record.sh).
#
# Aufruf: deploy.sh [<sha>]  — <sha> = Deploy-Ziel-Commit; Default: HEAD.
set -e

BINARY_NAME="solartown-adapter"
SERVICE_NAME="master-kanban-solartown"
SRC_DIR="/opt/stack/tools/portfolio/adapters/solartown"
REPO_DIR="/opt/stack"

SHA="${1:-$(git -C "${REPO_DIR}" rev-parse --short HEAD)}"
VERSION="$(git -C "${REPO_DIR}" describe --tags --always 2>/dev/null || echo "${SHA}")"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-X main.Version=${VERSION} -X main.Sha=${SHA} -X main.BuiltAt=${BUILT_AT}"

echo "Building ${BINARY_NAME}: version=${VERSION} sha=${SHA} built_at=${BUILT_AT}"
cd "${SRC_DIR}"
go build -ldflags "${LDFLAGS}" -o "/opt/stack/bin/${BINARY_NAME}.${SHA}" .

# Ledger-Zeile bei Deploy-Start (deploying); Probe/Reconciler bestätigt → live.
# probe_kind=cli: der Reconciler ruft `/opt/stack/bin/${BINARY_NAME} version --json`.
DEPLOY_METHOD="${DEPLOY_METHOD:-adapter-deploy-sh}" \
  "${REPO_DIR}/tools/portfolio/ledger-record.sh" "${BINARY_NAME}" cli "${VERSION}" "${SHA}" "/opt/stack/bin/${BINARY_NAME}"

echo "Atomic swap of binary to /opt/stack/bin/${BINARY_NAME}"
mv "/opt/stack/bin/${BINARY_NAME}.${SHA}" "/opt/stack/bin/${BINARY_NAME}"

echo "Restarting service ${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"

echo "Deploy of ${BINARY_NAME} completed successfully."
