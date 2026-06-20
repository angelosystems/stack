package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SageAction represents a logged Sage/Steward action
type SageAction struct {
	Action      string `json:"action"`       // "heal", "close-as-done", "escalate", "archive", "merge-nudge"
	Reason      string `json:"reason"`       // Why this action was taken
	HealCount   int    `json:"heal_count"`   // Current heal count
	IsLiveGeld  bool   `json:"is_live_geld"` // Whether this is a live-geld (quantbot) bead
	Timestamp   string `json:"timestamp"`
}

// SageDecisionEngine coordinates the healing and escalation of beads/workspaces
type SageDecisionEngine struct {
	Pool            *pgxpool.Pool
	DefaultMaxHeals int // Default N (usually 2)
}

func NewSageDecisionEngine(pool *pgxpool.Pool, defaultMaxHeals int) *SageDecisionEngine {
	if defaultMaxHeals <= 0 {
		defaultMaxHeals = 2
	}
	return &SageDecisionEngine{
		Pool:            pool,
		DefaultMaxHeals: defaultMaxHeals,
	}
}

// ProcessFailure evaluates a failed run for an initiative and decides whether to heal or escalate.
func (s *SageDecisionEngine) ProcessFailure(ctx context.Context, initiativeID string) (string, error) {
	// 1. Fetch the initiative details (firma/company, current heal_count)
	var firma string
	var healCount int
	err := s.Pool.QueryRow(ctx, 
		`SELECT firma, COALESCE(heal_count, 0) FROM portfolio.initiative WHERE id = $1`, 
		initiativeID).Scan(&firma, &healCount)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("initiative not found: %s", initiativeID)
		}
		return "", err
	}

	isLiveGeld := (firma == "quantbot")

	// 2. Apply Cockburn, Live-Geld-Konvention (quantbot beads only escalate, never close/heal/re-dispatch)
	if isLiveGeld {
		err = s.Escalate(ctx, initiativeID, "Live-Geld-Konvention: Trading-Path-Beads dürfen nur eskaliert werden", healCount, true)
		if err != nil {
			return "", err
		}
		return "escalated (live-geld)", nil
	}

	// 3. For regular beads, check the retry/healing budget (L4)
	if healCount >= s.DefaultMaxHeals {
		// STOP + Escalate!
		err = s.Escalate(ctx, initiativeID, fmt.Sprintf("Retry-Budget verbraucht (%d/%d erfolglose Heilungen)", healCount, s.DefaultMaxHeals), healCount, false)
		if err != nil {
			return "", err
		}
		return "escalated (budget-exhausted)", nil
	}

	// 4. Otherwise, perform healing/re-dispatch
	newHealCount := healCount + 1
	_, err = s.Pool.Exec(ctx, 
		`UPDATE portfolio.initiative SET heal_count = $1, updated_at = now() WHERE id = $2`, 
		newHealCount, initiativeID)
	if err != nil {
		return "", err
	}

	// Log healing action board-event
	payload := SageAction{
		Action:      "heal",
		Reason:      fmt.Sprintf("Automatisches Heilen / Re-dispatch eingeleitet (Heilversuch %d/%d)", newHealCount, s.DefaultMaxHeals),
		HealCount:   newHealCount,
		IsLiveGeld:  false,
		Timestamp:   time.Now().Format(time.RFC3339),
	}
	payloadBytes, _ := json.Marshal(payload)

	_, err = s.Pool.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'sage_action', 'master', $2, 'vk-sage')`,
		initiativeID, payloadBytes)
	if err != nil {
		return "", err
	}

	// Emit heal signal/log
	fmt.Printf("[Sage Advisor-Signal] HEAL: Initiative %s re-dispatched. Versuch %d/%d.\n", initiativeID, newHealCount, s.DefaultMaxHeals)

	return "healed", nil
}

// Escalate logs an escalation event and stops any future automatic action on the bead
func (s *SageDecisionEngine) Escalate(ctx context.Context, initiativeID string, reason string, healCount int, isLiveGeld bool) error {
	payload := SageAction{
		Action:      "escalate",
		Reason:      reason,
		HealCount:   healCount,
		IsLiveGeld:  isLiveGeld,
		Timestamp:   time.Now().Format(time.RFC3339),
	}
	payloadBytes, _ := json.Marshal(payload)

	// Insert the sage_action event to portfolio.initiative_event (makes it visible in Sage-Eskalation-Sicht)
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'sage_action', 'master', $2, 'vk-sage')`,
		initiativeID, payloadBytes)
	if err != nil {
		return err
	}

	// Emit advisor-Signal (which represents the alert/advisor-mail)
	fmt.Printf("[Sage Advisor-Signal/Mail] ESCALATION: Initiative %s eskaliert! Grund: %s\n", initiativeID, reason)

	return nil
}
