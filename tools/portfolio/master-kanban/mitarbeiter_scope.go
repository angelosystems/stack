package main

// Mitarbeiter-Firma-Scoping + Personen-Roster (PRD mk-user-management,
// Nachfolger von mitarbeiter-zugang W4).
//
// Quelle der Wahrheit ist das DB-Verzeichnis portfolio.person(+person_firma):
// jede SSO-Identitaet ist entweder Admin (ungescopt, god-view), Mitarbeiter
// (auf eine oder mehrere Firmen begrenzt) oder unbekannt. Die Env-Variablen
// sind nur noch Bootstrap:
//   MK_ADMIN_EMAILS       = a@x.de,b@x.de   → immer Admin (Aussperr-Schutz)
//   MK_MITARBEITER_SCOPE  = email=firma,…   → Legacy-Seed (nur falls Mail nicht
//                                             schon im DB-Roster)
//   MK_SCOPE_STRICT       = 1/true          → unbekannte SSO-Identitaet → 403
//
// MK_SCOPE_STRICT ist der Schalter fuer den End-Zustand "kein impliziter Admin".
// Default AUS = Verhalten wie bisher (unbekannte Mail = Admin), damit ein
// Flip niemanden aussperrt, bevor die Admins im Roster/Env stehen.
//
// Zentraler Waechter statt kopierter if-Bloecke: Lese-Endpunkte rufen
// filterInitiativesForScope / mitarbeiterBlocksRead, Schreib-Endpunkte
// mitarbeiterBlocksWrite; die Strict-Deny-Wand haengt als rosterGate-Middleware
// vor dem gesamten Mux. Die MCP-Schreibpfade (mcp.go) laufen ueber die
// HTTP-Endpunkte /api/move und /api/capture und sind damit automatisch mit
// gescoped.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// personScope: was aus dem Roster (DB oder Env-Bootstrap) ueber eine Identitaet
// bekannt ist. admin=true → ungescopt. Sonst begrenzt auf firmas (kann leer
// sein: dann sieht die Person nichts ausser ihr direkt Zugewiesenes).
type personScope struct {
	firmas []string
	admin  bool
	active bool
}

// roster: email(lowercase) -> personScope. nil/leer, wenn kein Verzeichnis.
var roster map[string]personScope

// scopeStrict: unbekannte SSO-Identitaet wird abgewiesen (MK_SCOPE_STRICT).
var scopeStrict bool

// errScopeDenied signalisiert, dass ein Schreibziel ausserhalb der
// Mitarbeiter-Firma liegt (wird vom HTTP-Handler auf 403 abgebildet).
var errScopeDenied = errors.New("scope: ziel liegt ausserhalb der mitarbeiter-firma")

// loadMitarbeiterScope baut das Roster beim Serve-Start: DB zuerst (Quelle der
// Wahrheit), dann Env-Bootstrap darueber (Admins + Legacy-Scope).
func loadMitarbeiterScope() {
	r := map[string]personScope{}
	if pool != nil {
		loadRosterFromDB(context.Background(), pool, r)
	}
	// Env-Admins: koennen nie ausgesperrt werden (ueberschreiben DB-Rolle nach oben).
	for _, e := range splitList(os.Getenv("MK_ADMIN_EMAILS")) {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		r[e] = personScope{admin: true, active: true}
	}
	// Legacy-Scope-Env nur als Seed, wenn die Mail nicht schon im Roster steht.
	for email, firma := range parseMitarbeiterScope(os.Getenv("MK_MITARBEITER_SCOPE")) {
		if _, ok := r[email]; ok {
			continue
		}
		r[email] = personScope{firmas: []string{firma}, active: true}
	}
	roster = r
	scopeStrict = truthy(os.Getenv("MK_SCOPE_STRICT"))
}

// reloadRoster laedt das Verzeichnis neu (nach Person-CRUD).
func reloadRoster() { loadMitarbeiterScope() }

// loadRosterFromDB liest portfolio.person(+person_firma) ins Roster. Als
// Funktions-Variable ausgelegt, damit Tests ohne Live-DB laufen (Unit-Tests
// setzen `roster` direkt und rufen loadMitarbeiterScope nie).
var loadRosterFromDB = func(ctx context.Context, p *pgxpool.Pool, dst map[string]personScope) {
	rows, err := p.Query(ctx, `
		SELECT p.email, p.role, p.active,
		       COALESCE(array_agg(pf.firma) FILTER (WHERE pf.firma IS NOT NULL), '{}') AS firmas
		  FROM portfolio.person p
		  LEFT JOIN portfolio.person_firma pf USING(email)
		 GROUP BY p.email, p.role, p.active`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "roster: DB-Load fehlgeschlagen (weiter mit Env-Bootstrap):", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var email, role string
		var active bool
		var firmas []string
		if err := rows.Scan(&email, &role, &active, &firmas); err != nil {
			fmt.Fprintln(os.Stderr, "roster: Zeile uebersprungen:", err)
			continue
		}
		dst[strings.ToLower(strings.TrimSpace(email))] = personScope{
			firmas: firmas, admin: role == "admin", active: active,
		}
	}
}

// splitList zerlegt "a,b , c" -> [a b c] (leere weg).
func splitList(raw string) []string {
	out := []string{}
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// truthy: 1/true/yes/on (case-insensitiv) -> true.
func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// parseMitarbeiterScope zerlegt "email=firma,email=firma" in eine Map
// (Legacy-Env-Bootstrap). Whitespace getrimmt, E-Mails lowercase. Leeres
// Ergebnis -> nil.
func parseMitarbeiterScope(raw string) map[string]string {
	m := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(kv[0]))
		firma := strings.TrimSpace(kv[1])
		if email == "" || firma == "" {
			continue
		}
		m[email] = firma
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// emailOf liefert die normalisierte SSO-Mail der Anfrage (lowercase, getrimmt).
func emailOf(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.Header.Get("X-Auth-Request-Email")))
}

// lookupPerson findet den personScope zur Anfrage-Identitaet.
func lookupPerson(r *http.Request) (personScope, bool) {
	if len(roster) == 0 {
		return personScope{}, false
	}
	email := emailOf(r)
	if email == "" {
		return personScope{}, false
	}
	sc, ok := roster[email]
	return sc, ok
}

// scopeFirmaFor liefert (firmas, true), wenn der Actor ein aktiver Mitarbeiter
// (nicht Admin) ist — dann sind Lesen/Schreiben auf diese Firmen begrenzt.
// Admins, Maschinen (X-Api-Key ohne Mail) und unbekannte Mails -> (nil,false):
// ungescopt wie bisher. Die Abweisung Unbekannter passiert separat im
// rosterGate (nur wenn MK_SCOPE_STRICT).
func scopeFirmaFor(r *http.Request) ([]string, bool) {
	sc, ok := lookupPerson(r)
	if !ok || sc.admin || !sc.active {
		return nil, false
	}
	return sc.firmas, true
}

// primaryScopeFirma liefert die erste Scope-Firma (oder ""), fuer Pfade, die
// genau eine Firma brauchen (capture/Neuanlage-Default).
func primaryScopeFirma(r *http.Request) string {
	if firmas, scoped := scopeFirmaFor(r); scoped && len(firmas) > 0 {
		return firmas[0]
	}
	return ""
}

// isAdminReq: darf diese Anfrage Verzeichnis-/Admin-Aktionen ausfuehren?
// Rostered-Admin ja. Maschine (gueltiger X-Api-Key, keine SSO-Mail) ja
// (checkAuth hat den Key bereits geprueft). Unbekannte oder Mitarbeiter-Mail: nein.
func isAdminReq(r *http.Request) bool {
	if sc, ok := lookupPerson(r); ok {
		return sc.admin && sc.active
	}
	return emailOf(r) == "" // Maschinenpfad (X-Api-Key)
}

// rosterDenies: in Strict-Mode wird eine SSO-Identitaet, die nicht (aktiv) im
// Roster steht, global abgewiesen. Maschinen-Calls (keine Mail) laufen durch —
// deren X-Api-Key prueft checkAuth an den Schreibrouten.
func rosterDenies(r *http.Request) bool {
	if !scopeStrict {
		return false
	}
	email := emailOf(r)
	if email == "" {
		return false
	}
	sc, ok := roster[email]
	return !ok || !sc.active
}

// mitarbeiterAPIAllow: Endpunkte, die ein gescopter Mitarbeiter erreichen
// darf. Alles andere unter /api/ ist fuer Mitarbeiter gesperrt (403) — Admin-
// und Maschinen-Flaechen (plan-edit, plan-git, plan-content, verwalter-chat,
// proposals, manager, sage, capacity, …) sind teils ungescopt und wuerden
// sonst ueber die SSO-Domain firmenfremde Inhalte leaken. Spiegel der
// crew-Broker-Allow-Liste plus Board-Lese-Endpunkte. Fail-closed: neue
// Endpunkte sind fuer Mitarbeiter erst nach bewusstem Eintrag erreichbar.
var mitarbeiterAPIAllow = map[string]bool{
	"/api/initiatives":     true,
	"/api/initiative":      true,
	"/api/create":          true,
	"/api/comment":         true,
	"/api/update":          true,
	"/api/move":            true,
	"/api/archive":         true,
	"/api/merge":           true,
	"/api/capture":         true,
	"/api/dispatch":        true,
	"/api/plan-approve":    true,
	"/api/whoami":          true,
	"/api/persons":         true,
	"/api/assign":          true,
	"/api/plans":           true, // Handler erzwingt fuer Gescopte ?initiative= + Scope-Check
	"/api/software-owners": true, // read-only; POST /api/software-owner bleibt draussen (admin)
	"/api/version":         true,
}

// rosterGate ist die Wand vor dem gesamten Mux: (1) Strict-Mode weist
// unbekannte SSO-Identitaeten ab, (2) gescopte Mitarbeiter erreichen nur die
// Allow-Liste. Admins und Maschinen (X-Api-Key) laufen ungefiltert durch.
func rosterGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rosterDenies(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":"identity_not_in_roster","hint":"Deine SSO-Identitaet ist im Master-Kanban-Verzeichnis nicht angelegt. Lass dich von einem Admin anlegen."}`)
			return
		}
		if _, scoped := scopeFirmaFor(r); scoped &&
			strings.HasPrefix(r.URL.Path, "/api/") && !mitarbeiterAPIAllow[r.URL.Path] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"endpoint_not_allowed_for_mitarbeiter","endpoint":%q,"hint":"Dieser Endpunkt ist Admin-Flaeche. Board-Arbeit laeuft ueber initiatives/create/comment/update/move/assign/dispatch/plan-approve."}`, r.URL.Path)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cardMatchesFirma: Karte gehoert zur (einzelnen) Firma, wenn die firma-Spalte
// passt ODER sie einen firma-Tag mit diesem Wert traegt (library-Karten).
func cardMatchesFirma(cardFirma string, firmaTags []string, scopeFirma string) bool {
	if scopeFirma == "" {
		return false
	}
	if cardFirma == scopeFirma {
		return true
	}
	for _, t := range firmaTags {
		if t == scopeFirma {
			return true
		}
	}
	return false
}

// cardMatchesFirmas: Karte gehoert zu EINER der Scope-Firmen (multi-firma).
func cardMatchesFirmas(cardFirma string, firmaTags, scopeFirmas []string) bool {
	for _, sf := range scopeFirmas {
		if cardMatchesFirma(cardFirma, firmaTags, sf) {
			return true
		}
	}
	return false
}

// initiativeInScope prueft via DB, ob eine Karte zu EINER der Firmen gehoert
// (Schreib-Pfad — firma-only, kein assigned-to-me). Funktions-Variable fuer
// DB-freie Tests. Nicht existierende IDs -> false (deny by default).
var initiativeInScope = func(ctx context.Context, p *pgxpool.Pool, id string, firmas []string) (bool, error) {
	var inScope bool
	err := p.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM portfolio.initiative i WHERE i.id=$1 AND i.firma = ANY($2)
		UNION ALL
		SELECT 1 FROM portfolio.initiative_tag t
		 WHERE t.initiative_id=$1 AND t.kind='firma' AND t.value = ANY($2)
	)`, id, firmas).Scan(&inScope)
	return inScope, err
}

// cardReadable prueft via DB, ob eine Karte fuer den Mitarbeiter LESBAR ist:
// eine der Firmen ODER ihm direkt zugewiesen (Owner der Karte / Assignee eines
// plan_items). Deckt die firmenuebergreifende Zuordnung ab. Funktions-Variable
// fuer DB-freie Tests.
var cardReadable = func(ctx context.Context, p *pgxpool.Pool, id string, firmas []string, selfEmail string) (bool, error) {
	var ok bool
	err := p.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM portfolio.initiative i
		 WHERE i.id=$1 AND (i.firma = ANY($2) OR lower(i.owner_email)=$3)
		UNION ALL
		SELECT 1 FROM portfolio.initiative_tag t
		 WHERE t.initiative_id=$1 AND t.kind='firma' AND t.value = ANY($2)
		UNION ALL
		SELECT 1 FROM portfolio.plan_item pi
		 WHERE pi.initiative_id=$1 AND lower(pi.assignee_email)=$3
	)`, id, firmas, selfEmail).Scan(&ok)
	return ok, err
}

// mitarbeiterBlocksWrite ist der Schreib-Waechter. true -> Antwort wurde bereits
// geschrieben, Handler muss return-en. Nicht-gescopte Identitaeten: false.
func mitarbeiterBlocksWrite(w http.ResponseWriter, r *http.Request, p *pgxpool.Pool, initID string) bool {
	firmas, scoped := scopeFirmaFor(r)
	if !scoped {
		return false
	}
	inScope, err := initiativeInScope(r.Context(), p, initID, firmas)
	if err != nil {
		http.Error(w, "scope-Pruefung fehlgeschlagen: "+err.Error(), 500)
		return true
	}
	if !inScope {
		http.Error(w, fmt.Sprintf(
			"403 — Karte %q liegt ausserhalb deiner Firma(en) %s. Du darfst nur Karten dieser Firma(en) aendern. Wenn das falsch ist, wende dich an einen Admin.",
			initID, strings.Join(firmas, ", ")), http.StatusForbidden)
		return true
	}
	return false
}

// planInitiativeID loest die Initiative eines Plans auf (Slug ODER Plan-ID).
// Funktions-Variable — DB-freie Tests. Leerer String = keine Initiative.
var planInitiativeID = func(ctx context.Context, p *pgxpool.Pool, planID string) string {
	var initID string
	_ = p.QueryRow(ctx,
		`SELECT initiative_id FROM portfolio.plan_item WHERE slug=$1 OR id=$1 LIMIT 1`,
		planID).Scan(&initID)
	return initID
}

// planApproveScopeBlocks haengt das Firma-Scoping in den Approve-Pfad ein.
// Sonderfall: gescopeter Request auf einen Plan ohne aufloesbare Initiative
// -> 403 (Firma nicht bestimmbar). Ungescopte Requests laufen durch.
func planApproveScopeBlocks(w http.ResponseWriter, r *http.Request, p *pgxpool.Pool, planID string) bool {
	firmas, scoped := scopeFirmaFor(r)
	if !scoped {
		return false
	}
	initID := planInitiativeID(r.Context(), p, planID)
	if initID == "" {
		http.Error(w, fmt.Sprintf(
			"403 — Plan %q hat noch keine Initiative, die Firma ist nicht bestimmbar. Als Mitarbeiter (%s) kannst du nur Plaene deiner Firma approven. Lass den Plan zuerst von einem Admin verdrahten.",
			planID, strings.Join(firmas, ", ")), http.StatusForbidden)
		return true
	}
	return mitarbeiterBlocksWrite(w, r, p, initID)
}

// mitarbeiterCreateFirma bestimmt die Firma fuer eine Neuanlage unter Scope.
// blocked==true -> Anfrage bereits mit 403 beendet. Nicht gescopte Mails:
// Body-Firma unveraendert. Mitarbeiter: fehlende Body-Firma -> erste Scope-Firma;
// fremde Body-Firma -> 403.
func mitarbeiterCreateFirma(w http.ResponseWriter, r *http.Request, bodyFirma string) (string, bool) {
	firmas, scoped := scopeFirmaFor(r)
	if !scoped {
		return bodyFirma, false
	}
	if strings.TrimSpace(bodyFirma) == "" {
		if len(firmas) > 0 {
			return firmas[0], false
		}
		http.Error(w, "403 — dir ist keine Firma zugeordnet; Neuanlage nicht moeglich. Wende dich an einen Admin.", http.StatusForbidden)
		return "", true
	}
	for _, f := range firmas {
		if bodyFirma == f {
			return bodyFirma, false
		}
	}
	http.Error(w, fmt.Sprintf(
		"403 — du darfst nur in deiner/deinen Firma(en) %s anlegen, nicht fuer %q. Lass die firma weg (dann wird %s gesetzt).",
		strings.Join(firmas, ", "), bodyFirma, firstOr(firmas, "")), http.StatusForbidden)
	return "", true
}

func firstOr(ss []string, def string) string {
	if len(ss) > 0 {
		return ss[0]
	}
	return def
}

// mitarbeiterBlocksRead ist der Einzel-Lese-Waechter. Out-of-scope UND
// nicht-zugewiesen -> 404 (existiert fuer den Mitarbeiter nicht).
func mitarbeiterBlocksRead(w http.ResponseWriter, r *http.Request, p *pgxpool.Pool, initID string) bool {
	firmas, scoped := scopeFirmaFor(r)
	if !scoped {
		return false
	}
	ok, err := cardReadable(r.Context(), p, initID, firmas, emailOf(r))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return true
	}
	if !ok {
		http.Error(w, "initiative nicht gefunden: "+initID, http.StatusNotFound)
		return true
	}
	return false
}

// filterInitiativesForScope behaelt nur lesbare Karten: eine der Firmen
// (Spalte oder firma-Tag) ODER dem Mitarbeiter direkt zugewiesen (owner_email
// der Karte / self in assignee_emails). Wird vor der Anreicherung angewandt.
func filterInitiativesForScope(items []map[string]any, firmas []string, selfEmail string) []map[string]any {
	self := strings.ToLower(strings.TrimSpace(selfEmail))
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		cardFirma, _ := item["firma"].(string)
		if cardMatchesFirmas(cardFirma, firmasTags(item["firmas"]), firmas) {
			out = append(out, item)
			continue
		}
		if self != "" {
			if owner, _ := item["owner_email"].(string); strings.ToLower(strings.TrimSpace(owner)) == self {
				out = append(out, item)
				continue
			}
			if jsonArrayHasEmail(item["assignee_emails"], self) {
				out = append(out, item)
				continue
			}
		}
	}
	return out
}

// firmasTags entpackt den JSON-Array-Wert "firmas" der Summary zu []string.
func firmasTags(firmas any) []string {
	arr, ok := firmas.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// jsonArrayHasEmail prueft, ob ein JSON-Array-Feld (z.B. assignee_emails) die
// (lowercased) Mail enthaelt.
func jsonArrayHasEmail(v any, self string) bool {
	arr, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range arr {
		if s, ok := e.(string); ok && strings.ToLower(strings.TrimSpace(s)) == self {
			return true
		}
	}
	return false
}
