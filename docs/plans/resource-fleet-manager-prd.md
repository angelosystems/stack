---
title: Resource-Fleet-Manager — Collector-Topologie & Datensatz-Owner
slug: resource-fleet-manager
status: approved
layer: prd
parent_plan: /opt/stack/docs/plans/capacity-governor-prd.md
scope: Festlegung der Collector-Topologie (Push vs. Pull) für Host-Ressourcen im Verbund mit dem Kapazitäts-Governor und dem Cockpit (Tab).
created: 2026-06-20
review:
  quick: auto
references:
  - /opt/stack/docs/plans/capacity-governor-prd.md
  - /opt/stack/docs/plans/vk-sage-workspace-steward-prd.md
---

# PRD: Resource-Fleet-Manager — Collector-Topologie

## 1. Problemstellung & Motivation

Für die dynamische Steuerung der Agenten-Kapazitäten durch den **Kapazitäts-Governor** (siehe `capacity-governor-prd.md`) sowie für die Visualisierung der Systemgesundheit im **Cockpit (Tab)** werden verlässliche Host-Metriken (CPU-Last, RAM-Belegung, Disk-Space) benötigt. 

Dabei stellt sich die Frage nach der optimalen Collector-Topologie:
- **Push-basiert**: Ein lokaler Collector-Prozess sammelt die Host-Daten und pusht sie aktiv in einen zentralen Datenspeicher (z.B. Postgres `kpi_events`).
- **Pull-basiert**: Ein zentraler Scraper fragt in regelmäßigen Intervallen HTTP-Endpunkte auf allen Ziel-Hosts ab (analog zu Prometheus).

Zusätzlich muss eine klare Verantwortlichkeit für die Systemmetriken definiert werden, um ineffiziente Doppel-Reads aus `/proc` und Inkonsistenzen bei der Ressourcen-Überwachung zu vermeiden.

---

## 2. Code-Analyse am realen System

Die Analyse des realen Mission-Control-Codes unter `/opt/missioncontrol/mission-control/publishers/infra-collector.go` liefert die architektonische Entscheidungsgrundlage.

### Implementierungsdetails in `infra-collector.go`:
1. **Verbindung & Initialisierung**:
   Der Collector stellt eine direkte Verbindung zu einer PostgreSQL-Instanz her:
   ```go
   const pgConnStr = "postgres://quantbot@127.0.0.1:54330/quantbot?sslmode=disable"
   ```
2. **Publisher-Instanziierung**:
   Es wird ein Push-Publisher aus dem gemeinsamen Fabric-Paket instanziiert:
   ```go
   pub, err := fabric.NewPublisher(pgConnStr)
   ```
3. **Edge-Trigger & Intervall-Push**:
   Der Collector liest in einem periodischen Loop (standardmäßig alle 30 Sekunden) CPU-, RAM- und Disk-Werte direkt über native Systemaufrufe bzw. das `/proc`-Dateisystem:
   - CPU über `/proc/stat`
   - RAM über `/proc/meminfo`
   - Disk über `syscall.Statfs`
4. **Push-Emission**:
   Die gesammelten KPIs werden über die Push-Methode `pub.EmitKPI` direkt in die Datenbank geschrieben:
   ```go
   if err := pub.EmitKPI(ctx, fabric.KPI{
       Owner:     "infra",
       Cluster:   "infra",
       Name:      name,
       Label:     label,
       Value:     value,
       Unit:      unit,
       MaxAgeSec: maxAge,
       Severity:  sev,
       Spark:     spark,
       DetailURL: "http://localhost:3200/d/infra",
   }); err != nil {
       log.Printf("emit %s: %v", name, err)
   }
   ```

---

## 3. Topologie-Entscheidung: PUSH

Basierend auf der realen Implementierung wird **Push** als die kanonische Collector-Topologie für Solartown / Mission-Control festgelegt:

*   **Entscheidung**: Der Collector (`infra-collector` / `angelo-infra-collector`) fungiert als aktiver **Publisher** und pusht System-KPIs direkt in denselben Store (PostgreSQL-Datenbank), den auch das Master-Kanban-Backend und nachgelagerte Tools lesen.
*   **Begründung**:
    *   **Kompatibilität**: Die Infrastruktur besitzt mit `fabric.NewPublisher` bereits ein mächtiges Push-SDK. Eine Pull-Architektur würde das Hinzufügen von HTTP-Servern auf jedem Host, Port-Freigaben und Service-Discovery-Mechanismen erzwingen, was unnötige Komplexität erzeugt.
    *   **Netzwerk & Firewalls**: Push-Verbindungen erfordern nur ausgehende Verbindungen vom Host zum zentralen Postgres-Port (54330/5434), wodurch das Sicherheitskonzept einfach und robust bleibt.

---

## 4. Kanonischer Datensatz-Owner & Konsumtion

Um die Effizienz der Host-Ressourcen zu wahren und Race-Conditions oder Messabweichungen auszuschließen, wird die Daten-Ownership strikt geregelt:

```
+-------------------------------------------------------+
|                    Echter Host                        |
|  /proc/stat   /proc/meminfo   syscall.Statfs (Disk)   |
+-------------------------------------------------------+
                           |
                     (Exklusiv-Read)
                           v
+-------------------------------------------------------+
|          infra-collector / angelo-collector           | (Kanonischer Owner)
+-------------------------------------------------------+
                           |
                  (Push via EmitKPI)
                           v
+-------------------------------------------------------+
|               Zentraler Postgres-Store                | (Single Source of Truth)
+-------------------------------------------------------+
              /                           
         (Konsumtion)                 (Konsumtion)
            v                             v
+-----------------------+     +-------------------------+
|   Kapazitäts-Governor |     |      Cockpit (Tab)      |
+-----------------------+     +-------------------------+
```

### Strikte Ownership-Regeln:

1.  **Kanonischer Datensatz-Owner**:
    Der lokale `infra-collector` (oder `angelo-infra-collector`) ist der **exklusive, kanonische Owner** der Host-Ressourcen-Metriken.
2.  **Kein Doppel-Read von `/proc`**:
    Andere System-Komponenten — insbesondere der **Kapazitäts-Governor** (im Reactor-Sweep) und das **Cockpit (UI-Tab)** — dürfen `/proc/stat`, `/proc/meminfo` oder Speicherstatistiken **niemals selbst auslesen**. Alle Konsumenten müssen die vom Collector im zentralen Store hinterlegten Daten abfragen.
3.  **Konsumtion durch den Governor**:
    Der Kapazitäts-Governor liest das für seine Berechnungen nötige Speicherbudget (`MemAvailable` etc.) und CPU-Auslastung ausschließlich über SQL-Queries aus der zentralen Postgres-Datenbank (`kpi_events` oder äquivalente KPI-Tabellen).
4.  **Konsumtion durch das Cockpit (Tab)**:
    Die Benutzeroberfläche des Cockpits liest die Systemmetriken ebenfalls ausschließlich aus dem zentralen DB-Store, um eine konsistente Anzeige im Host-Gesundheits-Tab zu gewährleisten.
