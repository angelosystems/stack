---
title: werkbank — Workspace-Plattform-Box provisioniert
slug: werkbank-provision
status: delivered
layer: delivery
parent_plan: -
sapling: st-vmvnq
created: 2026-06-18
---

# werkbank — Provisioning-Delivery

## Outcome

Dedizierte hcloud-Box `werkbank` (Coder + zukünftige Workspace-Services), getrennt
von werkstatt (Stay-Awesome-Apps) und den QuantBot-/Vault-Boxen. Skript:
`tools/werkbank-provision.sh` — idempotent re-runnable.

## Server-Fakten

| Feld | Wert |
|---|---|
| hcloud-Name | `werkbank` |
| Typ | `ccx33` (8 vCPU dedicated, 32 GB RAM, 240 GB disk) |
| Image | `ubuntu-24.04` |
| Datacenter | `nbg1-dc3` (Nuremberg) |
| IPv4 | 167.233.82.201 |
| FQDN | `werkbank.stayawesome.app` (Cloudflare-proxied) |
| TLS | Cloudflare-Edge Universal-Cert (`*.stayawesome.app`, Let's Encrypt E7) |
| Origin-SSL | self-signed (`/etc/ssl/werkbank/`) — Cloudflare-Mode "Full" |
| SSH-Keys | gastown-prod-ed25519, mario@hetzner-all |
| Labels | purpose=workspace-platform, owner=stayawesome, sapling=st-vmvnq |

## Acceptance-Checks (bestanden)

- ✅ `curl -sSf https://werkbank.stayawesome.app/` → HTTP 200 mit validem TLS-Cert
- ✅ `docker run --rm hello-world` → erfolgreich
- ✅ Kein QuantBot (kein Paket, kein Service, kein Container, kein /opt/quant*)
- ✅ Kein Vault (kein Paket, kein Service, kein Container, kein /opt/vault, kein
  `vault`-Binary in PATH)

## Installierte Komponenten

- Docker CE 29.5.3 (+ buildx, compose-plugin)
- nginx (system-nginx, vhost `werkbank.stayawesome.app`)
- certbot + python3-certbot-nginx (für späteren Origin-Cert-Wechsel)
- ufw (default deny incoming, allow 22/80/443)

## Anmerkungen

- Cloudflare-proxied gewählt (statt direktes Let's Encrypt am Origin), weil
  Let's Encrypt's ACME-Endpoint zum Provisioning-Zeitpunkt mit 503/Connection-
  Reset reagierte und Cloudflare's Universal-Cert ohnehin den Stay-Awesome-
  App-Konvention entspricht (alle proxied A-Records in der Zone).
- Origin-Cert ist self-signed, 10 Jahre gültig — reicht für Cloudflare-"Full"-
  Mode (kein Strict). Wenn später Strict gewünscht: certbot mit DNS-01 oder
  Cloudflare-Origin-CA-Cert nachziehen.
