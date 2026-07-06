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
LDFLAGS="-X main.Version=${VERSION} -X main.Sha=${SHA} -X main.BuiltAt=${BUILT_AT}"

OUT="${OUT:-/opt/stack/bin/${BINARY_NAME}.${SHA}}"

echo "Building ${BINARY_NAME}: version=${VERSION} sha=${SHA} built_at=${BUILT_AT}"
cd "${SRC_DIR}"
go build -ldflags "${LDFLAGS}" -o "${OUT}" .

if [ "${BUILD_ONLY:-0}" = "1" ]; then
  echo "BUILD_ONLY=1 → Stage-Binary liegt unter ${OUT} (kein swap, kein restart)."
  exit 0
fi

echo "Atomic swap of binary to /opt/stack/bin/${BINARY_NAME}"
mv "${OUT}" "/opt/stack/bin/${BINARY_NAME}"

echo "Creating symlink /usr/local/bin/mk -> /opt/stack/bin/${BINARY_NAME}"
ln -sf "/opt/stack/bin/${BINARY_NAME}" /usr/local/bin/mk

echo "Restarting service ${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"

echo "Deploy of ${BINARY_NAME} completed successfully."
