package main

// User-Management-Endpunkte (PRD mk-user-management):
//   GET  /api/persons            — Roster listen (Picker; jeder eingeloggte)
//   POST /api/person             — anlegen/aendern (upsert)        [admin only]
//   POST /api/person/deactivate  — deaktivieren (active=false)     [admin only]
//   POST /api/assign             — Owner (Karte) / Assignee (PRD) setzen
//                                  [firma-gescopt via mitarbeiterBlocksWrite]
//
// Person-CRUD ist ueber den crew-Broker NICHT erreichbar (dessen Allow-Liste
// ist board-only) — ein Mitarbeiter kann sich so nicht selbst zum Admin machen.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// registerUserMgmt haengt die User-Management-Routen an den Default-Mux.
func registerUserMgmt(p *pgxpool.Pool) {
	http.HandleFunc("/api/persons", handlePersonsList(p))
	http.HandleFunc("/api/person", handlePersonUpsert(p))
	http.HandleFunc("/api/person/deactivate", handlePersonDeactivate(p))
	http.HandleFunc("/api/assign", handleAssign(p))
	http.HandleFunc("/api/whoami", handleWhoami())
	http.HandleFunc("/api/software-owners", handleSoftwareOwnersList(p))
	http.HandleFunc("/api/software-owner", handleSoftwareOwnerSet(p))
}

// autoOwnerFor bestimmt den Owner fuer eine Karten-Neuanlage: die anlegende
// SSO-Identitaet, wenn sie (aktiv) im Roster steht — sonst der erste
// Env-Admin (Maschinen-/CLI-Anlagen fallen auf Mario zurueck). Nie leer,
// solange MK_ADMIN_EMAILS gesetzt ist.
func autoOwnerFor(r *http.Request) string {
	if email := emailOf(r); email != "" {
		if sc, ok := roster[email]; ok && sc.active {
			return email
		}
	}
	admins := splitList(os.Getenv("MK_ADMIN_EMAILS"))
	if len(admins) > 0 {
		return strings.ToLower(admins[0])
	}
	return ""
}

// GET /api/software-owners — Software-Registry: software -> Owner (+Name) +
// Abteilung (Programm-Gruppe, "eine Ebene hoeher").
func handleSoftwareOwnersList(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "401 — Auth fehlt.", http.StatusUnauthorized)
			return
		}
		rows, err := p.Query(r.Context(), `
			SELECT so.software, COALESCE(so.owner_email,'') AS owner_email,
			       COALESCE(pn.display_name, so.owner_email, '') AS owner_name,
			       COALESCE(so.abteilung,'') AS abteilung
			  FROM portfolio.software_owner so
			  LEFT JOIN portfolio.person pn ON pn.email = so.owner_email
			 ORDER BY so.software`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var sw, email, name, abt string
			if err := rows.Scan(&sw, &email, &name, &abt); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			out = append(out, map[string]any{
				"software": sw, "owner_email": email, "owner_name": name, "abteilung": abt,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// POST /api/software-owner — Registry-Upsert. Body: {software, owner_email?,
// abteilung?}. Nur mitgesendete Felder werden geaendert (Pointer-Semantik);
// leerer String = Feld leeren. Sind danach Owner UND Abteilung leer, wird die
// Registry-Zeile entfernt. Programm-Ebene = admin only (Karten-Ownership
// bleibt fuer Mitarbeiter uebertragbar, das Programm-Mandat vergibt der Admin).
func handleSoftwareOwnerSet(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "401 — Auth fehlt.", http.StatusUnauthorized)
			return
		}
		if !isAdminReq(r) {
			http.Error(w, "403 — Software-Owner/Abteilung vergibt nur ein Admin.", http.StatusForbidden)
			return
		}
		var body struct {
			Software   string  `json:"software"`
			OwnerEmail *string `json:"owner_email"`
			Abteilung  *string `json:"abteilung"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "400 — ungueltiger Body: "+err.Error(), http.StatusBadRequest)
			return
		}
		sw := strings.TrimSpace(body.Software)
		if sw == "" {
			http.Error(w, "400 — software fehlt.", http.StatusBadRequest)
			return
		}
		if body.OwnerEmail == nil && body.Abteilung == nil {
			http.Error(w, "400 — owner_email oder abteilung angeben.", http.StatusBadRequest)
			return
		}
		// Owner validieren (nur wenn gesetzt und nicht-leer).
		if body.OwnerEmail != nil {
			owner := strings.ToLower(strings.TrimSpace(*body.OwnerEmail))
			*body.OwnerEmail = owner
			if owner != "" {
				var active bool
				if err := p.QueryRow(r.Context(),
					`SELECT active FROM portfolio.person WHERE email=$1`, owner).Scan(&active); err != nil {
					http.Error(w, fmt.Sprintf("400 — person %q ist nicht im Verzeichnis. Lege sie zuerst an.", owner), http.StatusBadRequest)
					return
				}
				if !active {
					http.Error(w, fmt.Sprintf("400 — person %q ist deaktiviert.", owner), http.StatusBadRequest)
					return
				}
			}
		}
		if body.Abteilung != nil {
			*body.Abteilung = strings.TrimSpace(*body.Abteilung)
		}
		// Partial-Upsert: COALESCE behaelt nicht mitgesendete Felder; NULLIF
		// macht leere Strings zu NULL (Feld leeren).
		var ownerArg, abtArg any
		if body.OwnerEmail != nil {
			ownerArg = *body.OwnerEmail
		}
		if body.Abteilung != nil {
			abtArg = *body.Abteilung
		}
		if _, err := p.Exec(r.Context(), `
			INSERT INTO portfolio.software_owner(software, owner_email, abteilung, updated_at)
			VALUES ($1, NULLIF($2,''), NULLIF($3,''), now())
			ON CONFLICT (software) DO UPDATE SET
			  owner_email = CASE WHEN $2::text IS NULL THEN portfolio.software_owner.owner_email ELSE NULLIF($2,'') END,
			  abteilung   = CASE WHEN $3::text IS NULL THEN portfolio.software_owner.abteilung   ELSE NULLIF($3,'') END,
			  updated_at  = now()`,
			sw, ownerArg, abtArg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Leere Zeilen (weder Owner noch Abteilung) aufraeumen.
		if _, err := p.Exec(r.Context(),
			`DELETE FROM portfolio.software_owner WHERE software=$1 AND owner_email IS NULL AND abteilung IS NULL`,
			sw); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintf(w, `{"ok":true,"software":%q}`, sw)
	}
}

// GET /api/whoami — Identitaet + Rolle der aktuellen Anfrage, damit die UI das
// Roster-Admin-Panel nur fuer Admins zeigt und den Picker sinnvoll defaultet.
func handleWhoami() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := emailOf(r)
		role, firmas := "", []string{}
		if sc, ok := lookupPerson(r); ok {
			firmas = sc.firmas
			if sc.admin {
				role = "admin"
			} else {
				role = "mitarbeiter"
			}
		}
		if firmas == nil {
			firmas = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"email": email, "role": role, "is_admin": isAdminReq(r), "firmas": firmas,
		})
	}
}

// GET /api/persons — Roster fuer den Picker (Name, Rolle, aktiv, Firmen).
func handlePersonsList(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "401 — Auth fehlt (SSO-Header oder X-Api-Key).", http.StatusUnauthorized)
			return
		}
		rows, err := p.Query(r.Context(), `
			SELECT p.email, p.display_name, p.role, p.active,
			       COALESCE(array_agg(pf.firma ORDER BY pf.firma) FILTER (WHERE pf.firma IS NOT NULL), '{}') AS firmas
			  FROM portfolio.person p
			  LEFT JOIN portfolio.person_firma pf USING(email)
			 GROUP BY p.email, p.display_name, p.role, p.active
			 ORDER BY p.active DESC, p.display_name`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var email, name, role string
			var active bool
			var firmas []string
			if err := rows.Scan(&email, &name, &role, &active, &firmas); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			out = append(out, map[string]any{
				"email": email, "display_name": name, "role": role,
				"active": active, "firmas": firmas,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// POST /api/person — upsert. Body: {email, display_name, role, firmas:[], active}.
func handlePersonUpsert(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "401 — Auth fehlt.", http.StatusUnauthorized)
			return
		}
		if !isAdminReq(r) {
			http.Error(w, "403 — nur Admins duerfen das Verzeichnis pflegen.", http.StatusForbidden)
			return
		}
		var body struct {
			Email       string   `json:"email"`
			DisplayName string   `json:"display_name"`
			Role        string   `json:"role"`
			Firmas      []string `json:"firmas"`
			Active      *bool    `json:"active"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "400 — ungueltiger Body: "+err.Error(), http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if email == "" || !strings.Contains(email, "@") {
			http.Error(w, "400 — email fehlt oder ist keine Mail-Adresse.", http.StatusBadRequest)
			return
		}
		role := strings.TrimSpace(body.Role)
		if role == "" {
			role = "mitarbeiter"
		}
		if role != "admin" && role != "mitarbeiter" {
			http.Error(w, "400 — role muss 'admin' oder 'mitarbeiter' sein.", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.DisplayName)
		if name == "" {
			name = email
		}
		active := true
		if body.Active != nil {
			active = *body.Active
		}

		tx, err := p.Begin(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer tx.Rollback(r.Context())

		if _, err := tx.Exec(r.Context(), `
			INSERT INTO portfolio.person(email, display_name, role, active, updated_at)
			VALUES ($1,$2,$3,$4, now())
			ON CONFLICT (email) DO UPDATE SET
			  display_name=EXCLUDED.display_name, role=EXCLUDED.role,
			  active=EXCLUDED.active, updated_at=now()`,
			email, name, role, active); err != nil {
			http.Error(w, "person upsert: "+err.Error(), 500)
			return
		}
		// Firmen komplett ersetzen (idempotent, deterministisch).
		if _, err := tx.Exec(r.Context(), `DELETE FROM portfolio.person_firma WHERE email=$1`, email); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, f := range body.Firmas {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			if _, err := tx.Exec(r.Context(),
				`INSERT INTO portfolio.person_firma(email, firma) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
				email, f); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		reloadRoster()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"email":%q,"role":%q,"active":%v}`, email, role, active)
	}
}

// POST /api/person/deactivate — Body: {email}. active=false; Zuordnungen bleiben.
func handlePersonDeactivate(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "401 — Auth fehlt.", http.StatusUnauthorized)
			return
		}
		if !isAdminReq(r) {
			http.Error(w, "403 — nur Admins duerfen das Verzeichnis pflegen.", http.StatusForbidden)
			return
		}
		var body struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "400 — ungueltiger Body: "+err.Error(), http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if email == "" {
			http.Error(w, "400 — email fehlt.", http.StatusBadRequest)
			return
		}
		tag, err := p.Exec(r.Context(),
			`UPDATE portfolio.person SET active=false, updated_at=now() WHERE email=$1`, email)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if tag.RowsAffected() == 0 {
			http.Error(w, "404 — person nicht gefunden: "+email, http.StatusNotFound)
			return
		}
		reloadRoster()
		fmt.Fprintf(w, `{"ok":true,"email":%q,"active":false}`, email)
	}
}

// POST /api/assign — Owner (Karte) oder Assignee (PRD/plan_item) setzen.
// Body: {target:"initiative"|"plan_item", id, person_email}. Leeres person_email
// = Zuordnung entfernen. Firma-gescopt: der Mitarbeiter darf nur in seiner Firma
// zuweisen (Guard auf der zugehoerigen Initiative).
func handleAssign(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "401 — Auth fehlt.", http.StatusUnauthorized)
			return
		}
		var body struct {
			Target      string `json:"target"`
			ID          string `json:"id"`
			PersonEmail string `json:"person_email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "400 — ungueltiger Body: "+err.Error(), http.StatusBadRequest)
			return
		}
		target := strings.TrimSpace(body.Target)
		id := strings.TrimSpace(body.ID)
		person := strings.ToLower(strings.TrimSpace(body.PersonEmail))
		if id == "" || (target != "initiative" && target != "plan_item") {
			http.Error(w, `400 — target muss "initiative" oder "plan_item" sein, id ist Pflicht.`, http.StatusBadRequest)
			return
		}
		// Zugewiesene Person muss existieren und aktiv sein (ausser Entfernen).
		if person != "" {
			var active bool
			err := p.QueryRow(r.Context(),
				`SELECT active FROM portfolio.person WHERE email=$1`, person).Scan(&active)
			if err != nil {
				http.Error(w, fmt.Sprintf("400 — person %q ist nicht im Verzeichnis. Lege sie zuerst an.", person), http.StatusBadRequest)
				return
			}
			if !active {
				http.Error(w, fmt.Sprintf("400 — person %q ist deaktiviert und kann nicht zugewiesen werden.", person), http.StatusBadRequest)
				return
			}
		}

		// Firma-Guard: die betroffene Initiative bestimmen und pruefen.
		initID := id
		if target == "plan_item" {
			initID = planInitiativeID(r.Context(), p, id)
			if initID == "" {
				http.Error(w, fmt.Sprintf("404 — plan_item %q hat keine Initiative.", id), http.StatusNotFound)
				return
			}
		}
		if mitarbeiterBlocksWrite(w, r, p, initID) {
			return // 403 bereits geschrieben
		}

		var col, table, whereCol string
		if target == "initiative" {
			table, col, whereCol = "portfolio.initiative", "owner_email", "id"
		} else {
			table, col, whereCol = "portfolio.plan_item", "assignee_email", "id"
		}
		var val any
		if person != "" {
			val = person
		} else {
			val = nil
		}
		tag, err := p.Exec(r.Context(),
			fmt.Sprintf(`UPDATE %s SET %s=$1 WHERE %s=$2`, table, col, whereCol), val, id)
		if err != nil {
			http.Error(w, "assign: "+err.Error(), 500)
			return
		}
		if tag.RowsAffected() == 0 {
			http.Error(w, fmt.Sprintf("404 — %s %q nicht gefunden.", target, id), http.StatusNotFound)
			return
		}

		// Event-Log an der Initiative (Zuordnungs-Historie).
		payload, _ := json.Marshal(map[string]any{
			"target": target, "id": id, "person_email": person, "field": col,
		})
		_, _ = p.Exec(r.Context(),
			`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
			 VALUES ($1,'assigned','master',$2,$3)`, initID, payload, actorFrom(r))

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"target":%q,"id":%q,"person_email":%q}`, target, id, person)
	}
}
