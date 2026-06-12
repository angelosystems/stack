# Plan — SSO Rollout via oauth2-proxy

**ADR:** `docs/decisions/0001-sso-oauth2-proxy.md`
**Erstellt:** 2026-04-30
**Eigentümer:** Mario von Gemünden
**Host:** `werkstatt` (178.104.255.22) — wird zentraler Edge für alle `*.stayawesome.app`

## Status: ✅ LIVE seit Session-Ende 2026-05-06

Erstes Tool unter SSO ist `finance.stayawesome.app` + Hub `app.stayawesome.app`.
Successor-ADR-0003 dokumentiert die Authentik-Migration für wachsende Tool-Landschaft.

## Stand jetzt — was ist auf werkstatt fertig

| Komponente | Status |
|---|---|
| nginx 1.24 läuft (an 178.104.255.22, ssh-tunnel-Konflikt umgangen) | ✅ |
| oauth2-proxy v7.15.2 (`/usr/local/bin/oauth2-proxy`, DynamicUser, hardened) | ✅ läuft |
| oauth2-proxy.cfg + cookie-secret.env + google-oauth.env (Client-ID + Secret eingespielt) | ✅ |
| Vault: `/root/.secrets/stayawesome/google-oauth-client.json` | ✅ |
| nginx-Snippets `oauth2-endpoints.conf` + `oauth2-require.conf` (wiederverwendbar) | ✅ |
| nginx-Vhosts: ACME-Catchall, auth, finance, app | ✅ alle enabled |
| Let's Encrypt SAN-Cert für auth+finance+app (DNS-01 via Cloudflare) | ✅ |
| Cloudflare DNS für auth/finance/app → 178.104.255.22 (orange-cloud) | ✅ |
| Login-Page rebranded (Stay-Awesome-Logo emerald, Banner, Footer) | ✅ |
| UserBadge global im Root-Layout (ADR-0002) | ✅ |
| Hub mit Tool-Sidebar + KI-Chat (`app.stayawesome.app/hub`) | ✅ |
| Tools-Registry `apps/fin/src/lib/tools-registry.ts` (single source of truth) | ✅ |

## Was Mario klicken muss — Reihenfolge

### Schritt 1 — Cloudflare DNS-Records

In Cloudflare-Dashboard → `stayawesome.app` → DNS:

| Type | Name | Content | Proxy |
|---|---|---|---|
| A | `auth` | `178.104.255.22` | 🟠 Proxied (orange) |
| A | `finance` | `178.104.255.22` | 🟠 Proxied (orange) |
| A | `app` | `178.104.255.22` | 🟠 Proxied (orange) |

**Optional** (wenn Hub-Pattern aus ADR später kommt — Schritt 7+):

| Type | Name | Content | Proxy |
|---|---|---|---|
| A | `app` | `178.104.255.22` | 🟠 Proxied |

### Schritt 2 — Google OAuth Client in GCP

1. Öffne https://console.cloud.google.com/apis/credentials
2. Stelle sicher das Projekt **`stayawesome-office`** oben gewählt ist (das mit `claude-admin` SA)
3. Wenn der **OAuth consent screen** noch nicht konfiguriert ist:
   - Klick **OAuth consent screen** (links) → **User Type: Internal** → Name z.B. „Stay Awesome SSO"
   - Authorized domains: `stayawesome.de`, `stayawesome.app`
   - Save
4. Zurück zu **Credentials** → **+ Create Credentials** → **OAuth client ID**
   - Application type: **Web application**
   - Name: `oauth2-proxy on werkstatt`
   - Authorized JavaScript origins: `https://auth.stayawesome.app`
   - **Authorized redirect URIs: `https://auth.stayawesome.app/oauth2/callback`** ← exakt so
   - Create
5. Kopiere die zwei Werte aus dem Popup:
   - **Client ID** (sieht aus wie `1234-abc.apps.googleusercontent.com`)
   - **Client Secret** (langer String)

### Schritt 3 — Werte an Claude geben

Schreib mir die zwei Werte hier in den Chat. Ich trage sie in
`/etc/oauth2-proxy/google-oauth.env` ein (root-only-readable) und starte oauth2-proxy.

**Sicherheit:** Diese Werte sind **kein** Banking-Geheimnis, aber auch nicht öffentlich. Mit
ihnen könnte jemand sich als „Stay Awesome SSO"-App ausgeben. Sobald sie eingespielt sind, lege
ich sie zusätzlich in `/root/.secrets/stayawesome/google-oauth-client.json` ab (Vault-Pattern).

## Was Claude dann macht — vollautomatisch

Sobald Schritte 1-3 durch sind, führt Claude diese Sequenz aus (ein Befehl):

1. **certbot installieren** (apt) und Cert für `auth.stayawesome.app` + `finance.stayawesome.app`
   anfordern via HTTP-01-Challenge auf `/.well-known/acme-challenge/` (Cloudflare proxied
   das durch).
2. **Vhosts enablen** (`auth.…` und `finance.…` per symlink in `sites-enabled/`).
3. **OAuth-Credentials einspielen** in `/etc/oauth2-proxy/google-oauth.env`.
4. **oauth2-proxy starten** (`systemctl start oauth2-proxy`).
5. **End-to-End-Test:**
   - `curl -I https://finance.stayawesome.app` → erwarte `302 → https://auth.stayawesome.app/...`
   - Browser-Test: Login mit `info@stayawesome.de` → finance-Portal zeigt Chat-UI mit
     User-Email im Header.

## Schritt 4+ — kommt nach erstem grünen Login

| # | Was | Wann |
|---|---|---|
| 4 | `packages/auth-ts/` Shared-Lib (Workspace-Groups-Lookup, `requireUser`, `hasRole`) | erste Iteration nach Login-Test |
| 5 | Documenso (`sign.…`) hinter oauth2-proxy + nginx-Header-Map zu Documenso-Format | wenn Documenso-Migration ansteht |
| 6 | `app.stayawesome.app` Hub-Skeleton (zunächst Alias auf `finance.…`) | wenn Hub-Pattern Phase 2 startet |
| 7 | Per-Tool-Group-Restriction (z.B. `finance@stayawesome.de` als nur-Finance-Schreib-Group) | wenn Mitarbeiter-Cross-Access geregelt werden muss |

## Files-Inventar auf werkstatt — wo lebt was

```
/usr/local/bin/oauth2-proxy                              # Binary (v7.15.2, sha256 verifiziert)
/etc/oauth2-proxy/
  oauth2-proxy.cfg                                       # Haupt-Konfig (provider=google, cookies, etc.)
  google-oauth.env                                       # Client-ID + Secret (TEMPLATE, fehlt noch)
  cookie-secret.env                                      # Cookie-Signing-Key (auto-generiert)
/etc/systemd/system/oauth2-proxy.service                 # systemd-Unit (DynamicUser, hardened)
/etc/nginx/snippets/
  oauth2-endpoints.conf                                  # /oauth2/* + 401-Redirect (server-level include)
  oauth2-require.conf                                    # auth_request + Header-Forwarding (location-level include)
/etc/nginx/sites-available/
  00-acme-and-redirect                                   # ENABLED — Port 80, ACME + 301-Redirect
  auth.stayawesome.app                                   # NOCH NICHT enabled (kein Cert)
  finance.stayawesome.app                                # NOCH NICHT enabled (kein Cert)
/var/www/acme/.well-known/acme-challenge/                # ACME challenge webroot
```

## Health-Checks für Future-Mario

- `systemctl status nginx oauth2-proxy` — beide active
- `curl -sI -H "Host: finance.stayawesome.app" http://localhost/` — sollte `302` zurückgeben (HTTPS-Redirect)
- `curl -sI https://finance.stayawesome.app/` — sollte `302` mit `Location: /oauth2/sign_in?...` (nicht eingeloggt)
- `curl -s https://auth.stayawesome.app/ping` — sollte `200 OK` (oauth2-proxy lebt)
- `journalctl -u oauth2-proxy -n 50 --no-pager` — Login-Versuche + Refresh-Cycles

## Memory-Pointer

Diese Konvention ist load-bearing für alle zukünftigen Stay-Awesome-Web-Tools. Sie steht in:
- ADR: `docs/decisions/0001-sso-oauth2-proxy.md`
- Memory-Reference (wird beim ersten erfolgreichen Login angelegt): `reference_stayawesome_sso.md`
