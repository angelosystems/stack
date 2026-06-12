# Documenso × Authentik — Optionen-Vergleich

Stand: 2026-05-02 · Status: ENTWURF — wartet auf Mario-Entscheidung vor Card-Spawn

> **Kontext:** Captable und Inbox laufen schon hinter Authentik. Documenso (sign.stayawesome.app) ist die letzte Stay-Awesome-App mit eigener Login-Form. Bevor wir blind eine Card spawnen, müssen wir uns für einen von zwei Wegen entscheiden — sie haben deutlich unterschiedliche Implikationen.

---

## Ausgangslage

- **Documenso v1.x self-hosted** auf Hetzner-VM `sa-sign` (IPv4 178.104.195.247, NBG1)
- **NICHT auf werkstatt** (wo Captable + Authentik + Inbox laufen)
- **UI-Signup deaktiviert** in Self-Hosted-Build → User müssen via `createUser()` in DB angelegt werden (siehe Memory `reference_documenso_self_hosted`)
- **Documenso supportet nativ OIDC** (Server-ENV `NEXT_PUBLIC_FEATURE_OIDC_PROVIDER_ENABLED=true` + SSO-Config)

## Option A — Forward-Auth-Schicht (analog Captable)

**Wie Captable**: nginx-Outpost-Snippet vor Documenso, X-Authentik-Email als Trusted-Header.

```
Browser → CF → werkstatt-nginx (forward-auth) → ??? → sa-sign-nginx → Documenso
```

**Probleme:**
- Documenso läuft auf **anderem Server** (sa-sign) — Authentik-Outpost müsste entweder auf sa-sign installiert werden ODER werkstatt müsste als Reverse-Proxy für sign.stayawesome.app dienen (Cross-Datacenter-Latenz, neuer SPOF)
- Documenso erkennt Trusted-Header nicht nativ → wir müssten den Code patchen wie bei Captable (Custom-CredentialsProvider)
- User müssen **trotzdem** in Documenso-DB existieren — Forward-Auth allein erstellt keine User. Provisioning bleibt manuell via `createUser()`.

**Aufwand:** hoch · **Eleganz:** 2/5 · **Reversibilität:** mittel

## Option B — Documenso OIDC direkt (empfohlen)

Documenso macht selbst OIDC-Client-Calls gegen Authentik, kein Trusted-Header-Hack:

```
Browser → CF → sa-sign-nginx → Documenso (OIDC-Client) → idp.stayawesome.app
```

**Vorteile:**
- Keine Code-Patches an Documenso (Native-Feature, nur ENVs setzen)
- Cross-Server-Setup ist Standard (Authentik exposed bereits HTTPS)
- **Just-in-Time-Provisioning möglich**: erste OIDC-Anmeldung legt User automatisch in Documenso-DB an (löst das `createUser()`-Problem)
- Login-Form bleibt verfügbar als Fallback (für Service-Accounts)

**Was zu tun:**
1. In Authentik: OIDC-Provider `documenso-oidc` (analog `captable-oidc` pk=1)
2. In Documenso: ENVs `NEXT_PUBLIC_FEATURE_OIDC_PROVIDER_ENABLED=true` + Client-ID/Secret
3. Container restart, OIDC-Login-Button erscheint auf `/signin`
4. JIT-User-Anlage testen mit Mario-Account
5. ADR-0010 + Session-Log

**Aufwand:** mittel · **Eleganz:** 4/5 · **Reversibilität:** hoch (alles additiv)

## Option C — Status quo

Documenso behält eigene Login-Form, User werden weiterhin via `createUser()` provisioniert.

**Vorteile:**
- Null Aufwand
- Documenso ist eine spezialisierte App (Vertrags-Signatur), nicht Daily-Use → SSO-Wert ist bei <5 Usern überschaubar
- Service-Account-Fallback ist eh nötig für Mario-Workflows

**Nachteile:**
- Mario muss sich Documenso-Passwort separat merken (Bitwarden hilft)
- Bei Offboarding muss in 2 Systemen deaktiviert werden (Authentik + Documenso)

## Empfehlung

**Option B** wenn SSO-Coherence wichtig ist (langfristig bei wachsendem Team), sonst **Option C**.

Wenn Mario zustimmt → ich spawne Card mit Option-B-Spec.

## Mario-Entscheidung erbitten

```
[ ] Option A (Forward-Auth) — nicht empfohlen, hoher Aufwand
[ ] Option B (OIDC direkt) — empfohlen, mittlerer Aufwand
[ ] Option C (Status quo) — null Aufwand, SSO-Lücke akzeptiert
```

Vaultwarden-Authentik-Card ist obsolet: Stay Awesome nutzt Bitwarden Hosted (vault.bitwarden.com), siehe Memory `reference_stayawesome_bitwarden`.
