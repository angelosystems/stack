package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Scope-Entscheidung des Approve-Endpunkts (/api/plan-approve), DB-frei.
//
// Ein voller Handler-Test ist wegen os.ReadFile/os.WriteFile + gitCommit +
// direktem planItem-DB-Zugriff zu schwer; getestet wird deshalb die isolierte
// Scope-Entscheidung planApproveScopeBlocks. Ihre beiden DB-Beruehrungen sind
// als Funktions-Variablen ersetzbar: planInitiativeID (Plan -> Initiative) und
// initiativeInScope (Initiative -> Firma, via mitarbeiterBlocksWrite). Muster:
// mitarbeiter_scope_test.go (withScope / withInScope).

// withPlanInitiative ersetzt die Plan->Initiative-Aufloesung fuer einen Test.
func withPlanInitiative(t *testing.T, fn func(ctx context.Context, p *pgxpool.Pool, planID string) string) {
	t.Helper()
	prev := planInitiativeID
	planInitiativeID = fn
	t.Cleanup(func() { planInitiativeID = prev })
}

// runApproveScope fuehrt planApproveScopeBlocks hinter einem Dummy-Handler aus
// und liefert den HTTP-Status (200, wenn nicht geblockt).
func runApproveScope(t *testing.T, email, planID string) int {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if planApproveScopeBlocks(w, r, nil, planID) {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL, nil)
	if email != "" {
		req.Header.Set("X-Auth-Request-Email", email)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

const scopeMail = "angelo.calcagno@stayawesome.de"

// (a) Mitarbeiter approved einen Plan fremder Firma -> 403.
func TestPlanApproveScope_ForeignFirma403(t *testing.T) {
	withScope(t, map[string]string{scopeMail: "stayawesome"})
	withPlanInitiative(t, func(context.Context, *pgxpool.Pool, string) string { return "qb-init" })
	withInScope(t, func(context.Context, *pgxpool.Pool, string, []string) (bool, error) {
		return false, nil // Initiative gehoert NICHT zur Firma
	})
	if code := runApproveScope(t, scopeMail, "qb-plan-prd"); code != http.StatusForbidden {
		t.Fatalf("fremde firma: want 403 got %d", code)
	}
}

// (b) Mitarbeiter approved einen Plan der EIGENEN Firma -> durch (kein 403).
func TestPlanApproveScope_OwnFirmaPasses(t *testing.T) {
	withScope(t, map[string]string{scopeMail: "stayawesome"})
	withPlanInitiative(t, func(context.Context, *pgxpool.Pool, string) string { return "sa-init" })
	withInScope(t, func(context.Context, *pgxpool.Pool, string, []string) (bool, error) {
		return true, nil // Initiative gehoert zur Firma
	})
	if code := runApproveScope(t, scopeMail, "sa-plan-prd"); code != http.StatusOK {
		t.Fatalf("eigene firma: want 200 got %d", code)
	}
}

// Sonderfall: gescopeter Request auf Plan ohne aufloesbare Initiative -> 403
// (Firma nicht bestimmbar). Der Waechter darf die Firma-DB gar nicht erst
// befragen, weil es nichts aufzuloesen gibt.
func TestPlanApproveScope_NoInitiative403(t *testing.T) {
	withScope(t, map[string]string{scopeMail: "stayawesome"})
	withPlanInitiative(t, func(context.Context, *pgxpool.Pool, string) string { return "" })
	withInScope(t, func(context.Context, *pgxpool.Pool, string, []string) (bool, error) {
		t.Fatal("ohne Initiative darf initiativeInScope nicht aufgerufen werden")
		return false, nil
	})
	if code := runApproveScope(t, scopeMail, "waise-plan-prd"); code != http.StatusForbidden {
		t.Fatalf("plan ohne initiative: want 403 got %d", code)
	}
}

// (c) Admin-Mail (nicht gemappt): unveraendert durch, keine DB-Beruehrung.
func TestPlanApproveScope_AdminUnchanged(t *testing.T) {
	withScope(t, map[string]string{scopeMail: "stayawesome"})
	withPlanInitiative(t, func(context.Context, *pgxpool.Pool, string) string {
		t.Fatal("planInitiativeID darf fuer Admin-Mail nicht aufgerufen werden")
		return ""
	})
	withInScope(t, func(context.Context, *pgxpool.Pool, string, []string) (bool, error) {
		t.Fatal("initiativeInScope darf fuer Admin-Mail nicht aufgerufen werden")
		return false, nil
	})
	if code := runApproveScope(t, "mario@stayawesome.de", "sa-plan-prd"); code != http.StatusOK {
		t.Fatalf("admin: want 200 (unveraendert) got %d", code)
	}
}

// (d) Leeres Mapping: niemand ist Mitarbeiter, unveraendert durch, keine DB.
func TestPlanApproveScope_EmptyMappingUnchanged(t *testing.T) {
	withScope(t, nil)
	withPlanInitiative(t, func(context.Context, *pgxpool.Pool, string) string {
		t.Fatal("bei leerem Mapping darf planInitiativeID nie aufgerufen werden")
		return ""
	})
	withInScope(t, func(context.Context, *pgxpool.Pool, string, []string) (bool, error) {
		t.Fatal("bei leerem Mapping darf die Firma-DB nie befragt werden")
		return false, nil
	})
	if code := runApproveScope(t, scopeMail, "sa-plan-prd"); code != http.StatusOK {
		t.Fatalf("leeres mapping: want 200 got %d", code)
	}
}
