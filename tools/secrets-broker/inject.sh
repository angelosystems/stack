#!/usr/bin/env sh
# inject.sh — fetch a secret from the broker and exec the workspace command
# with the secret in an env-var. The secret never touches the filesystem.
#
# Usage:
#   SECRETS_BROKER_URL=http://broker:8089 \
#   SECRETS_BROKER_TENANT=stayawesome \
#   SECRETS_BROKER_TOKEN=... \
#     inject.sh GMAIL_OAUTH gmail_oauth -- python app.py
#
# Args:
#   $1   env-var name to export inside the workspace
#   $2   secret name to request from the broker
#   --   separator
#   $@   the workspace command to exec
set -eu

env_name="$1"; shift
secret_name="$1"; shift
[ "${1:-}" = "--" ] || { echo "inject.sh: expected -- separator" >&2; exit 2; }
shift

: "${SECRETS_BROKER_URL:?must set SECRETS_BROKER_URL}"
: "${SECRETS_BROKER_TENANT:?must set SECRETS_BROKER_TENANT}"
: "${SECRETS_BROKER_TOKEN:?must set SECRETS_BROKER_TOKEN}"

resp=$(curl -fsS \
  -H "X-Tenant: ${SECRETS_BROKER_TENANT}" \
  -H "Authorization: Bearer ${SECRETS_BROKER_TOKEN}" \
  "${SECRETS_BROKER_URL}/v1/secret/${secret_name}")

value=$(printf '%s' "$resp" | sed -n 's/.*"value":"\([^"]*\)".*/\1/p')
[ -n "$value" ] || { echo "inject.sh: empty value for $secret_name" >&2; exit 3; }

export "${env_name}=${value}"
unset resp value SECRETS_BROKER_TOKEN
exec "$@"
