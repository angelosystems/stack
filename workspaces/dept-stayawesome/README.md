# Coder Workspace Template — `dept-stayawesome`

Initial template realising E4 of [`docs/plans/tenant-workspaces-prd.md`](../../docs/plans/tenant-workspaces-prd.md).
First tenant: Angelo / StayAwesome-SolarTown repo-set.

## Inhalt

| File | Zweck |
|---|---|
| `main.tf` | Coder-Terraform-Template: Agent, App (Claude Code), Docker-Container mit Mount-Whitelist und Capability-Drops. |
| `image/Dockerfile` | Workspace-Image — Debian + Node + Claude Code + `gt-plan`, unprivilegierter `coder`-User (UID 1000). |
| `test/negative-access.sh` | Negativ-Test: prüft im laufenden Workspace, dass `/opt/quantbot`, der globale Vault und fremde Tenant-Repos unerreichbar sind und `PLAN_REPO_ROOTS` korrekt gescopt ist. |

## Sicherheits-Invarianten

Aus PRD §5/§8 (D2, D3) umgesetzt im Template:

1. **Mount-Whitelist:** Genau ein Bind — `var.tenant_repos_host_path → var.workspace_repos_mount`. Kein `/opt`, kein `/root`, kein `docker.sock`.
2. **`PLAN_REPO_ROOTS`-Scope:** Im Agent-Env auf den Tenant-Mount fixiert, damit `gt-plan review` ausschließlich Tenant-Repos sieht.
3. **Unprivilegiert:** `user = "1000:1000"`, `privileged = false`, `cap_drop=[ALL]`, `no-new-privileges:true`.
4. **Boot-Guard:** Das `startup_script` bricht mit Exit 13 ab, falls `/opt/quantbot` oder `/root/.secrets` doch im Container sichtbar werden — Regressions-Sicherung.
5. **Secrets per Broker:** Keine Secrets im Image, keine im Template. Injektion erfolgt zur Startzeit durch den Per-Tenant-Broker (E3).

## Tenant-Onboarding (neue Abteilung)

Pro Tenant nur Werte ändern, kein Code-Edit:

```hcl
tenant                 = "dept-finance"
tenant_repos_host_path = "/srv/tenants/dept-finance/repos"
workspace_image        = "stack/coder-dept-finance:latest"
```

## Bauen & Verifizieren

```bash
# 1) Image bauen (auf werkbank, oder lokal mit Docker)
docker build -t stack/coder-dept-stayawesome:latest workspaces/dept-stayawesome/image

# 2) Coder-Template registrieren / aktualisieren
coder templates push dept-stayawesome -d workspaces/dept-stayawesome

# 3) Workspace starten und Negativ-Test ausführen
coder ssh ws-dept-stayawesome-angelo-dev -- \
  bash /home/coder/repos/stack/workspaces/dept-stayawesome/test/negative-access.sh
```

Der Negativ-Test endet mit `PASS`, wenn die Akzeptanz aus dem Sapling erfüllt
ist.
