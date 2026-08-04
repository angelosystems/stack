package main

import "testing"

// TestKartenPolitikMatrix — die vom Deep-Tech-Panel geforderte volle Matrix
// (mk-vorhaben-ebene WP-1, Ask A5 / fehlende Szenarien).
//
// Geprueft wird die zerlegte Karten-Entscheidung
//
//	isRootCard = parentID == "" && (pfadIstPlanDatei(path) || layerErlaubtKarte(layer))
//
// ueber layer x parent_plan. Dieser Test friert das HEUTIGE Verhalten ein: er
// ist die Regressionswand, gegen die WP-2 die Politik umstellt. Wer die
// Zielpolitik einbaut, muss die erwarteten Werte hier bewusst umschreiben —
// eine stille Verhaltensaenderung faellt damit auf.
func TestKartenPolitikMatrix(t *testing.T) {
	faelle := []struct {
		name     string
		layer    string
		path     string
		parentID string
		want     bool
	}{
		// prd — der Normalfall
		{"prd ohne Eltern, -prd.md", "prd", "/r/docs/plans/x-prd.md", "", true},
		{"prd MIT Eltern erbt heute die Karte des Dachs", "prd", "/r/docs/plans/x-prd.md", "dach", false},

		// vision/roadmap — bekommen heute eine Karte. WP-2 dreht das auf false.
		{"vision ohne Eltern", "vision", "/r/docs/vision/03-x.md", "", true},
		{"roadmap ohne Eltern", "roadmap", "/r/docs/roadmap/x.md", "", true},
		{"vision MIT Eltern", "vision", "/r/docs/vision/03-x.md", "dach", false},

		// Etappen — nie eine eigene Karte, ausser das Namensschema greift
		{"phase ohne Eltern, kein -prd.md", "phase", "/r/docs/plans/x-phase-1.md", "", false},
		{"epic ohne Eltern, kein -prd.md", "epic", "/r/docs/plans/x-epics.md", "", false},

		// Suffix-Zweig: Datei-Identitaet schlaegt durch, auch wenn die Politik
		// des layers allein keine Karte vorsaehe. Genau diese Vermischung war
		// der Grund fuer die Zerlegung — sie bleibt in WP-1 bewusst erhalten.
		{"phase MIT -prd.md-Suffix bekommt heute doch eine Karte", "phase", "/r/docs/plans/x-prd.md", "", true},
		{"implementation MIT -prd.md-Suffix", "implementation", "/r/docs/plans/x-prd.md", "", true},

		// Nicht-Karten-Layer ohne Suffix
		{"implementation ohne Eltern, kein Suffix", "implementation", "/r/docs/plans/x-implementation.md", "", false},
		{"delivery ohne Eltern, kein Suffix", "delivery", "/r/docs/plans/x-delivery.md", "", false},
		{"session ohne Eltern, kein Suffix", "session", "/r/docs/plans/x-session.md", "", false},

		// Unbekannte und leere layer — kein Absturz, Suffix entscheidet
		{"unbekannter layer MIT Suffix", "control-plane", "/r/docs/plans/x-prd.md", "", true},
		{"unbekannter layer ohne Suffix", "control-plane", "/r/docs/plans/x.md", "", false},
		{"leerer layer MIT Suffix", "", "/r/docs/plans/x-prd.md", "", true},
		{"leerer layer ohne Suffix", "", "/r/docs/plans/x.md", "", false},
	}

	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			got := f.parentID == "" && (pfadIstPlanDatei(f.path) || layerErlaubtKarte(f.layer))
			if got != f.want {
				t.Fatalf("layer=%q path=%q parent=%q: isRootCard=%v, erwartet %v",
					f.layer, f.path, f.parentID, got, f.want)
			}
		})
	}
}

// TestLayerKanon — bekannte Werte werden erkannt, unbekannte und leere gemeldet
// (sie werden NIE verworfen, sondern bekommen triage:layer-check).
func TestLayerKanon(t *testing.T) {
	bekannt := []string{"prd", "vision", "roadmap", "phase", "epic", "implementation", "delivery", "session"}
	for _, l := range bekannt {
		if !layerBekannt(l) {
			t.Errorf("layer %q sollte im Kanon stehen", l)
		}
	}

	// Aus dem Bestand tatsaechlich vorgefundene Ausreisser.
	unbekannt := []string{"", "control-plane", "experience", "runbook", "policy", "execution", "infra", "product", "research+control-plane"}
	for _, l := range unbekannt {
		if layerBekannt(l) {
			t.Errorf("layer %q sollte NICHT im Kanon stehen", l)
		}
	}
}

// TestZielpolitikIstDokumentiertAberInaktiv — haelt fest, dass WP-1 die
// Zielpolitik nur beschreibt und noch nicht vollzieht. Sobald WP-2 umstellt,
// faellt dieser Test und zwingt zur bewussten Entscheidung.
func TestZielpolitikIstDokumentiertAberInaktiv(t *testing.T) {
	for _, l := range []string{"vision", "roadmap"} {
		p := layerPolicies[l]
		if !p.hatKarte {
			t.Fatalf("layer %q: hatKarte ist bereits false — die Migration (WP-2) "+
				"wurde ohne Freeze-Protokoll und Link-Remap vollzogen", l)
		}
		if p.zielKarte {
			t.Fatalf("layer %q: zielKarte sollte false sein (wird zum Ziel, nicht zur Karte)", l)
		}
	}
	if p := layerPolicies["prd"]; !p.hatKarte || !p.zielKarte {
		t.Fatalf("prd muss heute und nach WP-2 eine Karte bekommen, hat %+v", p)
	}
}
