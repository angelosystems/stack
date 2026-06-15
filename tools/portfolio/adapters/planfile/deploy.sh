#!/bin/bash
set -e

SHA=$1
if [ -z "$SHA" ]; then
  echo "Error: No SHA provided"
  exit 1
fi

BINARY_NAME="planfile-adapter"
SERVICE_NAME="master-kanban-planfile"

echo "Building ${BINARY_NAME} for SHA: ${SHA}"
cd /opt/stack/tools/portfolio/adapters/planfile
go build -ldflags "-X main.Version=${SHA}" -o /opt/stack/bin/${BINARY_NAME}.${SHA} .

echo "Atomic swap of binary to /opt/stack/bin/${BINARY_NAME}"
mv /opt/stack/bin/${BINARY_NAME}.${SHA} /opt/stack/bin/${BINARY_NAME}

echo "Restarting service ${SERVICE_NAME}"
systemctl restart ${SERVICE_NAME}

echo "Deploy of ${BINARY_NAME} completed successfully."
