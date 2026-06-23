---
title: Secrets-Company-Topologie & OpenBao-Integration
slug: secrets-topologie
status: approved
layer: prd
parent_plan: null
scope: Definition der Secrets-Topologie im Portfolio: Abgrenzung privater vs. operativer Secrets, Trennung zwischen Companies, Setup von OpenBao auf der Vault-Box (178.105.55.184) im Namespace 'stayawesome' sowie die Spezifikation des Per-Tenant Secrets-Brokers (E3 st-4ugcp).
created: 2026-06-22
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [requirements, architecture]
references:
  - /opt/stack/docs/plans/tenant-onboarding-dept-stayawesome-delivery.md
---

# Secrets-Company-Topologie & OpenBao-Integration

## Kontext & Motivation

Im Rahmen der Bereitstellung isolierter Workspace-Templates (E4) und des Per-Tenant Secrets-Brokers (E3 st-4ugcp) ist eine klare, unmissverständliche Definition der Secrets-Topologie über das gesamte Portfolio hinweg erforderlich. 

Historisch bestand das Risiko einer unvollständigen Trennung zwischen persönlichen Zugangsdaten, operativen Passwörtern und Credentials verschiedener Mandanten (Tenants) oder Companies. Um den Blast-Radius bei potenziellen Sicherheitsvorfällen zu minimieren und eine saubere Compliance-Struktur zu etablieren, werden die Secrets entlang zweier Hauptachsen getrennt:

1. **Interaktive (Mensch) vs. Operative (Maschine) Secrets:**
   - **Private/Mensch-Secrets:** Passwörter, die ausschließlich von Personen interaktiv genutzt werden (z. B. persönliche Logins, private Admin-Zugänge). Diese verbleiben in Vaultwarden und werden niemals maschinell injiziert.
   - **Operative/Maschinen-Secrets:** API-Keys, Datenbank-Logins, OAuth-Tokens, die von autonomen Systemen, Workspaces oder Applikationen zur Laufzeit benötigt werden.

2. **Unternehmensgrenzen (Separation by Exclusion):**
   - Eine strikte logische und technische Schranke zwischen den operativen Systemen der verschiedenen Portfoliogesellschaften und Tenants.

---

## Strategische Entscheidungen (Mario-Entscheid 2026-06-18)

### 1. Plattform-Wahl & Hosting
Anstelle einer extern verwalteten Cloud-Lösung (wie Infisical-Orgs) wird **OpenBao** (die Open-Source-Abspaltung von HashiCorp Vault) auf der bestehenden, dedizierten und bereits unternehmenseigenen Vault-Box unter **`178.105.55.184`** betrieben. Dies garantiert vollständige Souveränität über die kryptografischen Schlüssel und Datenbestände.

### 2. Namespace-Struktur & Mandantentrennung
- Innerhalb von OpenBao wird ein übergeordneter Namespace namens **`stayawesome`** eingerichtet.
- Dieser Namespace dient ausschließlich operativen, maschinen-injizierbaren Secrets der StayAwesome-Infrastruktur.
- Innerhalb des `stayawesome`-Namespaces werden für jeden einzelnen Tenant (z. B. `dept-stayawesome` / Abteilungen) feingranulare **Sub-Policies** und Pfade definiert (z. B. `stayawesome/tenants/dept-stayawesome/...`).
- Eine mandantenübergreifende Abfrage wird auf Policy-Ebene im OpenBao-Kern blockiert.

### 3. Trennung durch Ausschluss (Isolation)
Zur drastischen Risikominimierung bleiben folgende Entitäten vollständig **außerhalb** des StayAwesome-OpenBao-Systems:
- **QuantBot:** Verwendet eine eigene, physisch und logisch komplett getrennte Datenbank- und Secret-Infrastruktur. Keine Berührungspunkte mit dem StayAwesome-Manager.
- **Solartown:** Bleibt ebenfalls vollständig außerhalb. Die Trennung erfolgt über dedizierte Rigs und getrennte Berechtigungskonstrukte.
- **Mario-privat:** Persönliche Zugangsdaten verbleiben im privaten Vaultwarden des Gründers und werden niemals automatisiert über den Secrets-Broker injiziert.

---

## Spezifikation Per-Tenant Secrets-Broker (E3 / st-4ugcp)

Der Secrets-Broker (`tools/secrets-broker`) fungiert als minimaler, in-process Vermittler für Workspace-Container. Er schützt den OpenBao-Hauptschlüssel vor dem direkten Zugriff aus flüchtigen Workspaces.

```
+-------------------------------------------------------------+
|                     OpenBao Box (178.105.55.184)            |
|  Namespace: stayawesome                                     |
|    - Path: /tenants/dept-stayawesome (Policy A)             |
|    - Path: /tenants/dept-marketing    (Policy B)             |
+-------------------------------------------------------------+
                               ^
                               | (AppRole Auth / TLS)
                               v
+-------------------------------------------------------------+
|                     Secrets-Broker                          |
|  - Liest OpenBao-Pfade basierend auf Tenant-Policy         |
|  - Validiert Client-Token pro Workspace                     |
|  - Haelt Secret-Werte nur fluechtig im Arbeitsspeicher      |
+-------------------------------------------------------------+
                               ^
                               | (HTTP API / HTTP Headers)
                               v
+-------------------------------------------------------------+
|                     Workspace Container                     |
|  - inject.sh fordert Secrets scoped fuer Tenant an          |
|  - Setzt SECRETS_BROKER_TOKEN fuer Authentifizierung        |
+-------------------------------------------------------------+
```

### Broker-Sicherheits-Garantien (D3 Constraints)
- **Zero-Disk footprint:** Keine echten Secret-Werte dürfen im Container-Image oder auf der lokalen Festplatte des Brokers im Klartext persistiert werden. Secrets existieren auf Workspace-Ebene nur als Umgebungsvariablen (`env-vars`).
- **Strict Tenant Isolation:** Versucht ein Workspace mit Token `tenant-A` ein Secret unter dem Pfad von `tenant-B` anzufragen, weist der Broker die Anfrage mit `403 Forbidden` ab.
- **Minimaler Scope:** Der Broker erlaubt dem Workspace nur Lesezugriff (`read`) auf explizit in der Policy deklarierte Secret-Keys.

---

## Arbeitspakete

Konvention: ⚒ Arbeit 1-5, ◆ Wert 1-5, ⚠ Gate, ◎ Eleganz 1-5.

### W1 — OpenBao Setup & Absicherung auf `178.105.55.184`   ⚒3 ◆5 ◎4
Installation und Grundkonfiguration von OpenBao auf der bestehenden Vault-Box.
- Bereitstellung über ein gehärtetes Systemd-Service- oder Docker-Setup.
- Absicherung des API-Endpunkts mit TLS-Zertifikaten.
- Initialisierung und sichere Verwahrung der Unseal-Keys (manuelles Entschlüsseln nach Reboots).
- ⚠ **Gate:** OpenBao ist gestartet und über TLS erreichbar.

### W2 — stayawesome Namespace & Sub-Policies   ⚒2 ◆4 ◎4
Konfiguration der logischen Struktur im Vault.
- Erstellung des globalen operativen Namespaces `stayawesome`.
- Einrichtung von AppRole-Authentifizierungsmechanismen für den Secrets-Broker.
- Erstellung feingranularer Lese-Policies für die einzelnen Tenants (z. B. `stayawesome/tenants/dept-stayawesome`).
- ⚠ **Gate:** Test-AppRole kann ausschließlich den eigenen Tenant-Pfad auslesen, Cross-Path-Lesezugriffe schlagen fehl.

### W3 — Secrets-Broker OpenBao-Anbindung   ⚒3 ◆5 ◎5
Erweiterung des bestehenden `tools/secrets-broker` (derzeit In-Memory `Store` aus Umgebungsvariablen).
- Integration eines OpenBao-Clients (unter Verwendung der offiziellen Bibliotheken).
- Umstellung der Secret-Abfrage von der lokalen Map auf Live-Abrufe aus dem OpenBao Namespace `stayawesome` unter Verwendung der Tenant-Identität.
- Beibehaltung der Acceptance-Kriterien im Broker-Test-Suite (`go test ./...` in `tools/secrets-broker`).
- ⚠ **Gate:** Erfolgreiches Bestehen aller automatisierten Tests gegen eine simulierte OpenBao-Instanz oder via Mock-Schnittstelle.

### W4 — Workspace-Injektion via `inject.sh`   ⚒2 ◆4 ◎4
Anpassung und Verifikation der Secret-Bereitstellung in den Workspaces beim Starten.
- Integration des `inject.sh` Skripts im Workspace-Onboarding-Template (z. B. `workspaces/dept-stayawesome`).
- Testen der Secret-Injektion: Umgebungsvariablen wie z. B. `GMAIL_OAUTH` müssen zur Laufzeit im Anwendungsprozess des Workspaces sauber geladen werden, ohne im Dockerfile oder auf der Platte zu landen.
- ⚠ **Gate:** Start eines Test-Workspaces schlägt fehl, wenn kein valides Broker-Token übergeben wird; startet erfolgreich und mit injizierten Werten bei korrektem Token.

---

## Erfolgs-Kriterien (messbar)

- [ ] OpenBao-Dienst läuft stabil auf `178.105.55.184` hinter TLS.
- [ ] Namespace `stayawesome` ist eingerichtet; Sub-Policies für Test-Tenants verweigern Cross-Namespace- und Cross-Tenant-Zugriffe.
- [ ] Der Secrets-Broker (`tools/secrets-broker`) nutzt OpenBao als Backend und liest Pfade dynamisch aus dem `stayawesome`-Namespace.
- [ ] `go test ./...` im Verzeichnis `tools/secrets-broker` läuft erfolgreich durch.
- [ ] Ein Workspace-Start in `workspaces/dept-stayawesome/` kann mittels `inject.sh` Secrets erfolgreich aus dem Broker laden.
- [ ] Nachweislich tauchen keine echten Secret-Werte in Git-Commits, Docker-Images oder unverschlüsselten Logfiles auf.

---

## Limitations & Abgrenzungen

- **Keine Replikation für QuantBot/Solartown:** Dieser OpenBao-Namespace ist strikt auf operative StayAwesome-Secrets begrenzt. QuantBot und Solartown haben eigene, getrennte Sicherheitskonzepte und nutzen diesen Broker nicht.
- **Keine Speicherung interaktiver Logins:** Keine Browser-Passwörter von Mitarbeitern oder persönliche Passwörter von Mario landen in OpenBao; diese verbleiben in Vaultwarden (Mensch-Schnittstelle).
- **Physischer Single-Point:** Die Vault-Box `178.105.55.184` stellt einen Single-Point-of-Failure dar. Aus diesem Grund müssen regelmäßige verschlüsselte Backups der OpenBao-Raft-Datenbank auf einen separaten Storage-Server durchgeführt werden.
