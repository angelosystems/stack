package main

import "strings"

// Unlinked beschreibt ein Work-Item ohne Initiative-Zuhause — Eintrag der
// „Unlinked"-Lane (Capture-Completeness PRD, L1).
//
// Reason:
//   - "no_join_key": Bead hat weder spec_id noch plan:<slug>-Label (würde
//     ohne Detektor still gedroppt; genau das, was SC1 verbietet).
//   - "no_match":    Bead hat Join-Key, aber keine passende Initiative.
//   - "no_link":     vk-Workspace existiert, aber kein initiative_link.
type Unlinked struct {
	Kind   string // "bead" | "vk_workspace"
	Rig    string // Prefix wie "st" / "qu"; "" für vk-Workspaces
	Ref    string
	Reason string
}

// classifyBeads liefert für jeden Bead ohne Zuhause einen Unlinked-Eintrag.
// Im Gegensatz zum bisherigen scanRigBeads droppt diese Funktion KEINEN Bead
// still — auch leere Join-Keys werden als "no_join_key" sichtbar gemacht.
func classifyBeads(rigPrefix string, beads []beadRow, slugToInitiative map[string]string) []Unlinked {
	var out []Unlinked
	for _, b := range beads {
		slug := getJoinKey(b.SpecID, b.Labels)
		if slug == "" {
			out = append(out, Unlinked{Kind: "bead", Rig: rigPrefix, Ref: b.ID, Reason: "no_join_key"})
			continue
		}
		if _, ok := slugToInitiative[strings.ToLower(slug)]; !ok {
			out = append(out, Unlinked{Kind: "bead", Rig: rigPrefix, Ref: b.ID, Reason: "no_match"})
		}
	}
	return out
}

// vkWorkspace ist die Detektor-Sicht auf einen vibe-kanban-Task (id + title);
// reicht zur Klassifikation, ohne den vk-Adapter-Code zu duplizieren.
type vkWorkspace struct {
	ID    string
	Title string
}

// classifyWorkspaces meldet jeden vk-Workspace, dessen ID nicht in
// linkedRefs auftaucht.
func classifyWorkspaces(workspaces []vkWorkspace, linkedRefs map[string]bool) []Unlinked {
	var out []Unlinked
	for _, ws := range workspaces {
		if !linkedRefs[ws.ID] {
			out = append(out, Unlinked{Kind: "vk_workspace", Ref: ws.ID, Reason: "no_link"})
		}
	}
	return out
}
