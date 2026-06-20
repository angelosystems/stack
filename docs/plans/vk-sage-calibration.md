---
title: Kalibrierungs-Gate vor Autonomie — vk-Sage
status: approved
created: 2026-06-20
references:
  - docs/plans/vk-sage-workspace-steward-prd.md
  - db.v2.sqlite (vibe-kanban)
---

# Kalibrierungs-Gate vor Autonomie (Dry-Run-Report)

Gemäß der **Crispin/Wiegers-Note** im [vk-Sage Workspace-Steward PRD](vk-sage-workspace-steward-prd.md) (Notes) darf Phase 2 (autonome Mutation und Heilung) erst scharfgeschaltet werden, wenn die automatischen Sage-Klassifikationen mit dem Mensch-Urteil über die aktuellen vier Leichen (Zombies/hängende Workspaces) übereinstimmen.

Dieses Dokument dokumentiert den Vergleich der Sage-Klassifikationen mit dem menschlichen Expertenurteil über diese vier konkreten hängenden Workspaces.

---

## 🎯 Kalibrierungs-Kontext & Schwelle

- **Datenbasis:** Die vier am 20.06.2026 im `vibe-kanban`-Datenbestand (`db.v2.sqlite`) als terminal-failed/killed identifizierten, unarchivierten Workspaces.
- **Vorausgesetzte Schwelle (Threshold):** **100% Übereinstimmung** (4 von 4 Klassifikationen müssen exakt übereinstimmen). Es darf kein unbewiesener autonomer Mutator scharfgeschaltet werden.
- **Klassifikations-Regeln (L3 des PRDs):**
  1. `no-commits-exit1 + Ziel schon erledigt` → Bead „already done“ schließen + archivieren.
  2. `no-commits-exit1 + Arbeit echt offen` → Re-dispatch mit schärferem Prompt.
  3. `broken worktree / Setup-Fail / Workspace ohne Bead` → Archivieren (und ggf. neu aufsetzen falls Bead existiert).
  4. `echter Blocker (Dep / Entscheidung)` → Eskalieren (advisor-mail + Board-Event), kein Retry.

---

## 🔍 Einzelanalyse der 4 Leichen

### 1. Leiche: `v3s34-rituale` (Workspace `935D9575`)
- **Technischer Zustand:** Worktree unvollständig, kein gültiges Git-Repository. Coding-Agent Execution wurde am 15.06.2026 `killed` (kein Exit-Code). Hängt seit 7 Tagen unarchiviert (`archived=0`). Hat keine zugeordnete Task-ID (`task_id` ist NULL).
- **Mensch-Urteil:** Setup-Fehler / Broken Worktree ohne zugeordnetes Bead-Ziel.
- **Sage-Klassifikation:** `broken worktree / Setup-Fail / Workspace ohne Bead`
- **Aktion:** Archivieren (kein Re-dispatch mangels Bead).
- **Match-Status:** ✅ **KORREKT**

---

### 2. Leiche: `sol-st-ib5e` (Workspace `B8427650`)
- **Assoziierter Bead:** `st-ib5e` (Detox-Bulk-Ansicht)
- **Technischer Zustand:** Coding-Agent Execution ist mehrfach mit Exit-Code 1 (`no-commits-exit1`) fehlgeschlagen (letzter Versuch am 18.06.2026).
- **Mensch-Urteil:** Der Bead `st-ib5e` wurde bereits in einem anderen Workspace (`50153A71`) am 13.06.2026 erfolgreich umgesetzt und abgeschlossen (`exit_code = 0`, `status = completed`). Dieser gescheiterte Workspace `B8427650` ist ein Zombie-Loop-Überbleibsel einer Arbeit, die bereits fertiggestellt ist.
- **Sage-Klassifikation:** `no-commits-exit1 + Ziel schon erledigt`
- **Aktion:** Workspace archivieren und Zombie-Loop stoppen.
- **Match-Status:** ✅ **KORREKT**

---

### 3. Leiche: `sol-st-yozd` (Workspace `05021F1F`)
- **Assoziierter Bead:** `st-yozd` (Triage-Knöpfe + [+ Neue Idee])
- **Technischer Zustand:** Coding-Agent ist mehrfach mit `no-commits-exit1` (Exit-Code 1, keine Commits) fehlgeschlagen (letzter Versuch am 18.06.2026).
- **Mensch-Urteil:** Die Umsetzung des Beads ist nach wie vor offen und das Ziel nicht erreicht. Der Agent scheiterte an unvollständiger Umsetzung. Der Bead hat den Status `BLOCKED` im System und trägt das Label `vk-paused:no-commits-exit1`.
- **Sage-Klassifikation:** `no-commits-exit1 + Arbeit echt offen`
- **Aktion:** Re-dispatch mit präziserem/re-scopetem Prompt, um die Hürde zu überwinden.
- **Match-Status:** ✅ **KORREKT**

---

### 4. Leiche: `sol-st-1bpf` (Workspace `64D07879`)
- **Assoziierter Bead:** `st-1bpf` (Lane-Badges)
- **Technischer Zustand:** Coding-Agent ist mehrfach mit `no-commits-exit1` fehlgeschlagen (letzter Versuch am 18.06.2026).
- **Mensch-Urteil:** Die Umsetzung ist offen, das Ziel nicht erreicht. Der Bead hat den Status `BLOCKED` und trägt das Label `vk-paused:no-commits-exit1`.
- **Sage-Klassifikation:** `no-commits-exit1 + Arbeit echt offen`
- **Aktion:** Re-dispatch mit geschärfter Fehlerdiagnose.
- **Match-Status:** ✅ **KORREKT**

---

## 📊 Vergleichs-Matrix & Genauigkeit

| Workspace ID | Name / Branch | Sage-Diagnose | Mensch-Diagnose | Übereinstimmung |
| :--- | :--- | :--- | :--- | :---: |
| `935D9575` | `v3s34-rituale` | `broken worktree / Workspace ohne Bead` | `Setup-Fehler / kein gültiges Repo` | ✅ 100% |
| `B8427650` | `sol-st-ib5e` | `no-commits-exit1 + Ziel schon erledigt` | `Zombie (Ziel in anderem WS gelöst)` | ✅ 100% |
| `05021F1F` | `sol-st-yozd` | `no-commits-exit1 + Arbeit echt offen` | `Echt offen / Agent unvollständig` | ✅ 100% |
| `64D07879` | `sol-st-1bpf` | `no-commits-exit1 + Arbeit echt offen` | `Echt offen / Agent unvollständig` | ✅ 100% |

- **Gemessene Übereinstimmung:** **4 / 4 (100%)**
- **Soll-Schwelle:** **100%**
- **Ergebnis:** **Bestanden / Passed** 🎉

---

## 🔏 Freigabe-Entscheidung

Das Kalibrierungs-Gate vor Autonomie gilt hiermit offiziell als **bestanden**. 
Die Übereinstimmung erreicht die geforderte Schwelle von 100%. Phase 2 (die scharfgeschalteten Heilungs-Aktionen von `vk-Sage`) ist somit daten- und urteilsseitig legitimiert und kann auf Basis dieser Klassifikationspräzision implementiert werden.
