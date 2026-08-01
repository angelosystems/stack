package main

// WP-4c Emit-Bruecke (PRD solartown fabrik-urteilsrollen-umbau, Panel BL-2):
// Jeder terminale Deploy-Ausgang wird als factory.deploy-verdikt nach
// town.events (:5433) gemeldet — mit dem deterministischen Smoke-Verdikt und
// der Identitaet der laufenden Binary (Aufloesung der Kopien-Unklarheit).
// BEST-EFFORT NACH der Deploy-Transaktion: ein Emit-Fehlschlag blockiert nie
// den Deploy; laeuft die Bruecke ins Leere, schlaegt der Abnahme-Waechter
// (merged-ohne-Deploy, :5434) von der anderen Seite an — bewusst
// gegenlaeufig redundant (PRD-Risikoabsatz Go/Python-Grenze).

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

func townPgURI() string {
	if v := os.Getenv("TOWN_PG_URI"); v != "" {
		return v
	}
	if v := os.Getenv("REACTOR_PG_URI"); v != "" {
		return v
	}
	return "postgres://remote:remote@127.0.0.1:5433/solartown_clean?sslmode=disable"
}

// buildDeployVerdiktPayload ist pur und testbar.
func buildDeployVerdiktPayload(service, gitSha, environment, verdict, smoke, why string) map[string]any {
	binary, _ := os.Executable()
	sha7 := gitSha
	if len(sha7) > 7 {
		sha7 = sha7[:7]
	}
	p := map[string]any{
		"service":      service,
		"git_sha":      gitSha,
		"environment":  environment,
		"verdict":      verdict, // live | rolled_back | errored
		"smoke":        smoke,   // green | red | none
		"binary":       binary,
		"incident_key": "deploy-" + service + "-" + sha7,
	}
	if why != "" {
		p["why"] = why
	}
	return p
}

func (r *reactor) emitDeployVerdikt(service, gitSha, environment, verdict, smoke, why string) {
	payload := buildDeployVerdiktPayload(service, gitSha, environment, verdict, smoke, why)
	body, err := json.Marshal(payload)
	if err != nil {
		r.logf("deploy-verdikt marshal miss (non-fatal): %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, townPgURI())
	if err != nil {
		r.logf("deploy-verdikt emit miss (non-fatal, Waechter faengt): %v", err)
		return
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx,
		"SELECT town.emit($1, $2::jsonb, 'deploy-reactor')",
		"factory.deploy-verdikt", string(body)); err != nil {
		r.logf("deploy-verdikt emit miss (non-fatal, Waechter faengt): %v", err)
	}
}
