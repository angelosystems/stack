#!/usr/bin/env bash
# verification script for twenty crm deployment prep on deploy box (178.105.36.33)
set -euo pipefail

DEPLOY_IP="178.105.36.33"

echo "=== Verifying connection to deploy box ($DEPLOY_IP) ==="
if ! ssh -o BatchMode=yes -o ConnectTimeout=5 "$DEPLOY_IP" "echo 'SSH Connection successful'"; then
  echo "Error: Cannot connect to $DEPLOY_IP via SSH." >&2
  exit 1
fi

echo "=== Verifying directory /opt/twenty exists on deploy ==="
if ! ssh "$DEPLOY_IP" "test -d /opt/twenty"; then
  echo "Error: Directory /opt/twenty does not exist on deploy." >&2
  exit 1
fi
echo "✓ Directory /opt/twenty exists."

echo "=== Verifying configuration files inside /opt/twenty ==="
if ! ssh "$DEPLOY_IP" "test -f /opt/twenty/docker-compose.yml" || ! ssh "$DEPLOY_IP" "test -f /opt/twenty/.env"; then
  echo "Error: Missing docker-compose.yml or .env in /opt/twenty on deploy." >&2
  exit 1
fi
echo "✓ Configuration files (docker-compose.yml and .env) are present."

echo "=== Verifying Docker Network 'twenty_default' exists on deploy ==="
if ! ssh "$DEPLOY_IP" "docker network inspect twenty_default >/dev/null 2>&1"; then
  echo "Error: Docker network twenty_default not found." >&2
  exit 1
fi
echo "✓ Docker network twenty_default exists."

echo "=== Verifying Internet Egress / NAT Masquerade on 'twenty_default' network ==="
echo "Testing raw IP outbound traffic (ping 8.8.8.8)..."
if ! ssh "$DEPLOY_IP" "docker run --rm --network twenty_default alpine ping -c 3 8.8.8.8 >/dev/null 2>&1"; then
  echo "Error: Container inside twenty_default network cannot ping 8.8.8.8 (no egress)." >&2
  exit 1
fi
echo "✓ Raw IP outbound ping succeeded."

echo "Testing DNS resolution and domain outbound traffic (ping google.com)..."
if ! ssh "$DEPLOY_IP" "docker run --rm --network twenty_default alpine ping -c 3 google.com >/dev/null 2>&1"; then
  echo "Error: Container inside twenty_default network cannot resolve or ping google.com (DNS issues)." >&2
  exit 1
fi
echo "✓ DNS resolution and outbound domain ping succeeded."

echo "=== Verification complete! /opt/twenty is prepared and Docker NAT/network has full egress. ==="
