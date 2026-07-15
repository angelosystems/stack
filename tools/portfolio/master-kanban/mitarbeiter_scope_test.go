package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Diese Tests laufen ohne Live-Board: die reine Zugehoerigkeits-Logik ist
// DB-frei, und der Schreib-/Lese-Waechter bekommt seinen DB-Zugriff
// (initiativeInScope / cardReadable) durch eine Fake-Funktion ersetzt.

// withScope setzt ein einfaches email->firma-Mapping als Mitarbeiter-Roster
// (jede Mail = aktiver Mitarbeiter mit genau dieser Firma) und stellt zurueck.
func withScope(t *testing.T, m map[string]string) {
	t.Helper()
	prev := roster
	if m == nil {
		roster = nil
	} else {
		r := map[string]personScope{}
		for email, firma := range m {
			r[strings.ToLower(email)] = personScope{firmas: []string{firma}, active: true}
		}
		roster = r
	}
	t.Cleanup(func() { roster = prev })
}

// withRoster setzt ein volles Roster (fuer Admin/multi-firma/inaktiv-Faelle).
func withRoster(t *testing.T, r map[string]personScope) {
	t.Helper()
	prev := roster
	roster = r
	t.Cleanup(func() { roster = prev })
}

// withStrict schaltet MK_SCOPE_STRICT fuer einen Test.
func withStrict(t *testing.T, on bool) {
	t.Helper()
	prev := scopeStrict
	scopeStrict = on
	t.Cleanup(func() { scopeStrict = prev })
}

// withInScope ersetzt den Schreib-DB-Zugriff des Waechters (firma-only).
func withInScope(t *testing.T, fn func(ctx context.Context, p *pgxpool.Pool, id string, firmas []string) (bool, error)) {
	t.Helper()
	prev := initiativeInScope
	initiativeInScope = fn
	t.Cleanup(func() { initiativeInScope = prev })
}

func TestParseMitarbeiterScope(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"leer -> nil", "", nil},
		{"nur whitespace -> nil", "   ,  ", nil},
		{"einzeln + lowercase + trim", " Angelo.Calcagno@stayawesome.de = stayawesome ",
			map[string]string{"angelo.calcagno@stayawesome.de": "stayawesome"}},
		{"mehrere", "a@x.de=stayawesome,b@x.de=solartown",
			map[string]string{"a@x.de": "stayawesome", "b@x.de": "solartown"}},
		{"ungueltige paare uebersprungen", "kaputt,c@x.de=stayawesome,=leer,d@x.de=",
			map[string]string{"c@x.de": "stayawesome"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMitarbeiterScope(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("len: want %d got %d (%v)", len(tc.want), len(got), got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("key %q: want %q got %q", k, v, got[k])
				}
			}
		})
	}
}

func TestScopeFirmaFor(t *testing.T) {
	withScope(t, map[string]string{"angelo.calcagno@stayawesome.de": "stayawesome"})

	// Gemappte Mail (auch bei abweichender Gross-/Kleinschreibung im Header).
	req := httptest.NewRequest("POST", "/api/move", nil)
	req.Header.Set("X-Auth-Request-Email", "Angelo.Calcagno@stayawesome.de")
	if firmas, scoped := scopeFirmaFor(req); !scoped || len(firmas) != 1 || firmas[0] != "stayawesome" {
		t.Errorf("gemappt: want ([stayawesome],true) got (%v,%v)", firmas, scoped)
	}

	// Nicht gemappte Mail (unbekannt, non-strict) -> kein Scope (Admin-Verhalten).
	req2 := httptest.NewRequest("POST", "/api/move", nil)
	req2.Header.Set("X-Auth-Request-Email", "fremd@stayawesome.de")
	if _, scoped := scopeFirmaFor(req2); scoped {
		t.Error("unbekannt: want scoped=false")
	}

	// Kein Header -> kein Scope.
	req3 := httptest.NewRequest("POST", "/api/move", nil)
	if _, scoped := scopeFirmaFor(req3); scoped {
		t.Error("ohne header: want scoped=false")
	}
}

// Admin-Rolle -> ungescopt; Multi-Firma -> beide Firmen; inaktiv -> kein Scope.
func TestScopeFirmaFor_RolesAndMultiFirma(t *testing.T) {
	withRoster(t, map[string]personScope{
		"admin@x.de":  {admin: true, active: true},
		"multi@x.de":  {firmas: []string{"stayawesome", "quantbot"}, active: true},
		"inaktiv@x.de": {firmas: []string{"stayawesome"}, active: false},
	})

	adminReq := httptest.NewRequest("POST", "/", nil)
	adminReq.Header.Set("X-Auth-Request-Email", "admin@x.de")
	if _, scoped := scopeFirmaFor(adminReq); scoped {
		t.Error("admin: want scoped=false (god-view)")
	}

	multiReq := httptest.NewRequest("POST", "/", nil)
	multiReq.Header.Set("X-Auth-Request-Email", "multi@x.de")
	firmas, scoped := scopeFirmaFor(multiReq)
	if !scoped || len(firmas) != 2 {
		t.Fatalf("multi: want 2 firmen scoped got (%v,%v)", firmas, scoped)
	}

	inaktivReq := httptest.NewRequest("POST", "/", nil)
	inaktivReq.Header.Set("X-Auth-Request-Email", "inaktiv@x.de")
	if _, scoped := scopeFirmaFor(inaktivReq); scoped {
		t.Error("inaktiv: want scoped=false")
	}
}

// Strict-Mode: unbekannte SSO-Mail wird abgewiesen; bekannte + Maschine nicht.
func TestRosterDenies_Strict(t *testing.T) {
	withRoster(t, map[string]personScope{
		"angelo.calcagno@stayawesome.de": {firmas: []string{"stayawesome"}, active: true},
		"admin@x.de":                     {admin: true, active: true},
		"weg@x.de":                       {firmas: []string{"stayawesome"}, active: false},
	})

	mk := func(email string) *http.Request {
		r := httptest.NewRequest("GET", "/api/initiatives", nil)
		if email != "" {
			r.Header.Set("X-Auth-Request-Email", email)
		}
		return r
	}

	// Strict AUS: nie abweisen (Backward-Compat, kein Lockout).
	withStrict(t, false)
	if rosterDenies(mk("wildfremd@x.de")) {
		t.Error("strict aus: darf nie abweisen")
	}

	// Strict AN.
	withStrict(t, true)
	if !rosterDenies(mk("wildfremd@x.de")) {
		t.Error("strict an: unbekannte Mail muss abgewiesen werden")
	}
	if !rosterDenies(mk("weg@x.de")) {
		t.Error("strict an: deaktivierte Person muss abgewiesen werden")
	}
	if rosterDenies(mk("angelo.calcagno@stayawesome.de")) {
		t.Error("strict an: bekannter Mitarbeiter darf nicht abgewiesen werden")
	}
	if rosterDenies(mk("admin@x.de")) {
		t.Error("strict an: Admin darf nicht abgewiesen werden")
	}
	if rosterDenies(mk("")) {
		t.Error("strict an: Maschine (keine Mail) darf nicht abgewiesen werden")
	}
}

// isAdminReq: Rostered-Admin ja, Maschine ja, Mitarbeiter/unbekannt nein.
func TestIsAdminReq(t *testing.T) {
	withRoster(t, map[string]personScope{
		"admin@x.de": {admin: true, active: true},
		"ma@x.de":    {firmas: []string{"stayawesome"}, active: true},
	})
	mk := func(email string) *http.Request {
		r := httptest.NewRequest("POST", "/api/person", nil)
		if email != "" {
			r.Header.Set("X-Auth-Request-Email", email)
		}
		return r
	}
	if !isAdminReq(mk("admin@x.de")) {
		t.Error("admin: want true")
	}
	if isAdminReq(mk("ma@x.de")) {
		t.Error("mitarbeiter: want false")
	}
	if isAdminReq(mk("unbekannt@x.de")) {
		t.Error("unbekannte SSO-Mail: want false")
	}
	if !isAdminReq(mk("")) {
		t.Error("maschine (keine Mail, X-Api-Key-Pfad): want true")
	}
}

func TestCardMatchesFirma(t *testing.T) {
	cases := []struct {
		cardFirma string
		tags      []string
		scope     string
		want      bool
	}{
		{"stayawesome", nil, "stayawesome", true},
		{"quantbot", nil, "stayawesome", false},
		{"code-factory", []string{"stayawesome"}, "stayawesome", true}, // library-Karte mit Tag
		{"quantbot", []string{"mariobrain"}, "stayawesome", false},
		{"stayawesome", nil, "", false}, // leerer Scope matcht nie
	}
	for _, tc := range cases {
		if got := cardMatchesFirma(tc.cardFirma, tc.tags, tc.scope); got != tc.want {
			t.Errorf("cardMatchesFirma(%q,%v,%q)=%v want %v", tc.cardFirma, tc.tags, tc.scope, got, tc.want)
		}
	}
}

// Mitarbeiter-Mail listet nur eigene-Firma-Karten (0 fremde) — inkl.
// library-Karte mit firma=stayawesome-Tag.
func TestFilterInitiativesForScope_OnlyOwnFirma(t *testing.T) {
	items := []map[string]any{
		{"id": "sa-eins", "firma": "stayawesome", "firmas": []any{}},
		{"id": "qb-zwei", "firma": "quantbot", "firmas": []any{}},
		{"id": "mb-drei", "firma": "mariobrain", "firmas": []any{}},
		{"id": "cf-lib", "firma": "code-factory", "firmas": []any{"stayawesome"}}, // library + Tag
	}
	got := filterInitiativesForScope(items, []string{"stayawesome"}, "")
	if len(got) != 2 {
		t.Fatalf("want 2 karten got %d: %v", len(got), got)
	}
	for _, it := range got {
		id, _ := it["id"].(string)
		if id != "sa-eins" && id != "cf-lib" {
			t.Errorf("fremde karte in ergebnis: %q", id)
		}
	}
}

// Cross-Firma-Zuordnung: ein Assignee/Owner sieht GENAU seine zugewiesene
// Fremd-Firma-Karte — und NICHT die Nachbarkarte derselben Firma.
func TestFilterInitiativesForScope_AssignedToMe(t *testing.T) {
	self := "angelo.calcagno@stayawesome.de"
	items := []map[string]any{
		{"id": "sa-eigen", "firma": "stayawesome", "firmas": []any{}},
		{"id": "qb-owner", "firma": "quantbot", "firmas": []any{}, "owner_email": self},
		{"id": "qb-assignee", "firma": "quantbot", "firmas": []any{}, "assignee_emails": []any{self}},
		{"id": "qb-nachbar", "firma": "quantbot", "firmas": []any{}}, // NICHT zugewiesen
	}
	got := filterInitiativesForScope(items, []string{"stayawesome"}, self)
	ids := map[string]bool{}
	for _, it := range got {
		ids[it["id"].(string)] = true
	}
	if !ids["sa-eigen"] || !ids["qb-owner"] || !ids["qb-assignee"] {
		t.Fatalf("eigene + zugewiesene fehlen: %v", ids)
	}
	if ids["qb-nachbar"] {
		t.Fatal("Leak: nicht-zugewiesene Fremd-Firma-Nachbarkarte im Ergebnis")
	}
	if len(got) != 3 {
		t.Fatalf("want 3 karten got %d", len(got))
	}
}

// Schreibversuch auf fremde Karte -> 403 (handlungsleitende Meldung).
func TestMitarbeiterBlocksWrite_ForeignCard403(t *testing.T) {
	withScope(t, map[string]string{"angelo.calcagno@stayawesome.de": "stayawesome"})
	withInScope(t, func(ctx context.Context, p *pgxpool.Pool, id string, firmas []string) (bool, error) {
		return false, nil
	})

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mitarbeiterBlocksWrite(w, r, nil, "qb-fremd") {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL, nil)
	req.Header.Set("X-Auth-Request-Email", "angelo.calcagno@stayawesome.de")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("fremde karte: want 403 got %d", resp.StatusCode)
	}
}

// In-Scope-Karte wird durchgelassen (kein Block).
func TestMitarbeiterBlocksWrite_OwnCardPasses(t *testing.T) {
	withScope(t, map[string]string{"angelo.calcagno@stayawesome.de": "stayawesome"})
	withInScope(t, func(ctx context.Context, p *pgxpool.Pool, id string, firmas []string) (bool, error) {
		return true, nil
	})

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mitarbeiterBlocksWrite(w, r, nil, "sa-eigen") {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL, nil)
	req.Header.Set("X-Auth-Request-Email", "angelo.calcagno@stayawesome.de")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("eigene karte: want 200 got %d", resp.StatusCode)
	}
}

// Admin-Mail (Roster-Admin): Waechter blockt nie und ruft die DB nicht auf.
func TestMitarbeiterBlocksWrite_AdminUnchanged(t *testing.T) {
	withRoster(t, map[string]personScope{
		"angelo.calcagno@stayawesome.de": {firmas: []string{"stayawesome"}, active: true},
		"mario@x.de":                     {admin: true, active: true},
	})
	withInScope(t, func(ctx context.Context, p *pgxpool.Pool, id string, firmas []string) (bool, error) {
		t.Fatal("initiativeInScope darf fuer Admin-Mail nicht aufgerufen werden")
		return false, nil
	})

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mitarbeiterBlocksWrite(w, r, nil, "qb-fremd") {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL, nil)
	req.Header.Set("X-Auth-Request-Email", "mario@x.de")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: want 200 (unveraendert) got %d", resp.StatusCode)
	}
}

// Neuanlage (/api/create): Mitarbeiter darf nur in der eigenen Firma anlegen.
func TestMitarbeiterCreateFirma(t *testing.T) {
	mail := "angelo.calcagno@stayawesome.de"

	newReq := func(email string) *http.Request {
		r := httptest.NewRequest("POST", "/api/create", nil)
		if email != "" {
			r.Header.Set("X-Auth-Request-Email", email)
		}
		return r
	}

	t.Run("mitarbeiter fremde firma -> 403", func(t *testing.T) {
		withScope(t, map[string]string{mail: "stayawesome"})
		w := httptest.NewRecorder()
		firma, blocked := mitarbeiterCreateFirma(w, newReq(mail), "quantbot")
		if !blocked {
			t.Fatalf("fremde firma: want blocked=true got firma=%q", firma)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("want 403 got %d", w.Code)
		}
	})

	t.Run("mitarbeiter eigene firma -> ok", func(t *testing.T) {
		withScope(t, map[string]string{mail: "stayawesome"})
		w := httptest.NewRecorder()
		firma, blocked := mitarbeiterCreateFirma(w, newReq(mail), "stayawesome")
		if blocked || firma != "stayawesome" {
			t.Fatalf("eigene firma: want (stayawesome,false) got (%q,%v)", firma, blocked)
		}
	})

	t.Run("mitarbeiter ohne firma -> default scope", func(t *testing.T) {
		withScope(t, map[string]string{mail: "stayawesome"})
		w := httptest.NewRecorder()
		firma, blocked := mitarbeiterCreateFirma(w, newReq(mail), "")
		if blocked || firma != "stayawesome" {
			t.Fatalf("ohne firma: want (stayawesome,false) got (%q,%v)", firma, blocked)
		}
	})

	t.Run("admin unveraendert", func(t *testing.T) {
		withRoster(t, map[string]personScope{
			mail:         {firmas: []string{"stayawesome"}, active: true},
			"mario@x.de": {admin: true, active: true},
		})
		w := httptest.NewRecorder()
		firma, blocked := mitarbeiterCreateFirma(w, newReq("mario@x.de"), "quantbot")
		if blocked || firma != "quantbot" {
			t.Fatalf("admin: want (quantbot,false) got (%q,%v)", firma, blocked)
		}
	})

	t.Run("leeres roster unveraendert", func(t *testing.T) {
		withScope(t, nil)
		w := httptest.NewRecorder()
		firma, blocked := mitarbeiterCreateFirma(w, newReq(mail), "quantbot")
		if blocked || firma != "quantbot" {
			t.Fatalf("leeres roster: want (quantbot,false) got (%q,%v)", firma, blocked)
		}
	})
}

// Leeres Roster = alles wie heute: niemand ist Mitarbeiter, kein Filter, kein
// Block — auch nicht fuer die spaeter gemappte Mail.
func TestEmptyRoster_NoScopeNoBlock(t *testing.T) {
	withScope(t, nil)
	withInScope(t, func(ctx context.Context, p *pgxpool.Pool, id string, firmas []string) (bool, error) {
		t.Fatal("bei leerem Roster darf die DB nie befragt werden")
		return false, nil
	})

	req := httptest.NewRequest("POST", "/api/move", nil)
	req.Header.Set("X-Auth-Request-Email", "angelo.calcagno@stayawesome.de")
	if _, scoped := scopeFirmaFor(req); scoped {
		t.Fatal("leeres roster: scopeFirmaFor darf nie scoped=true liefern")
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mitarbeiterBlocksWrite(w, r, nil, "qb-fremd") {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req2, _ := http.NewRequest("POST", srv.URL, nil)
	req2.Header.Set("X-Auth-Request-Email", "angelo.calcagno@stayawesome.de")
	resp, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("leeres roster: want 200 got %d", resp.StatusCode)
	}
}
