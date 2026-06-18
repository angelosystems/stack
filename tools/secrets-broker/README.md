# secrets-broker

Minimal in-process secrets broker for tenant isolation.

## Why

Workspaces (paperclip flows, polecat runners, app containers) need credentials
at runtime without baking them into images or writing them to disk. The broker
answers tenant-scoped requests over HTTP and the workspace consumes the
response as an env-var via `inject.sh`.

## Properties

- **Per-tenant policy** in `policy.yaml` declares which secret *names* each
  tenant may read and the auth token it must present.
- **Secret values live only in process memory** of the broker, populated from
  `SECRET_<TENANT>_<NAME>` env-vars at startup. Values are not in the image,
  not in the policy file, not in any on-disk artifact.
- **Cross-tenant access is rejected** at the handler — wrong token, wrong
  tenant header, or a secret name not listed under the tenant all return 403.

## Acceptance check

`go test ./...` covers the three sapling acceptance criteria:

1. `TestOwnTenantCanRead` — a workspace reads its own tenant's secret.
2. `TestCrossTenantAccessForbidden` — cross-tenant attempts (three vectors)
   return 403.
3. `TestNoSecretValuesOnDisk` — secret values loaded from env-vars never
   appear in any file under the simulated image root.

## Runtime

```
secrets-broker -addr :8089 -policy /etc/secrets-broker/policy.yaml
```

Workspace side:

```
SECRETS_BROKER_URL=http://broker:8089 \
SECRETS_BROKER_TENANT=stayawesome \
SECRETS_BROKER_TOKEN=... \
  ./inject.sh GMAIL_OAUTH gmail_oauth -- python app.py
```

## Upgrade path

Swap the in-process `Store` for a backend client (OpenBao, Infisical) without
changing the handler contract.
