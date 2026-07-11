package main

// /api/abos — Abo-Limits + Service-Registry für das Ressourcen-Panel.
// PRD: docs/plans/ressourcen-abo-panel-prd.md. Der Endpoint merged nur
// (Registry resources.yaml + Collector-Feeds), er misst nie selbst.

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type abosBinding struct {
	Service  string `yaml:"service" json:"service"`
	Box      string `yaml:"box" json:"box"`
	Verified *bool  `yaml:"verified" json:"verified,omitempty"`
}

type abosResource struct {
	ID       string        `yaml:"id" json:"id"`
	Kind     string        `yaml:"kind" json:"kind"`
	Plan     string        `yaml:"plan" json:"plan,omitempty"`
	Feed     string        `yaml:"feed" json:"feed"`
	Bindings []abosBinding `yaml:"bindings" json:"bindings"`
}

type abosRegistry struct {
	Resources []abosResource `yaml:"resources"`
}

type abosLimit struct {
	Window   string `json:"window"`
	Percent  int    `json:"percent"`
	Severity string `json:"severity"`
	ResetsAt int64  `json:"resets_at"`
}

type abosAccount struct {
	Name   string      `json:"name"`
	Label  string      `json:"label"`
	OK     bool        `json:"ok"`
	Source string      `json:"source"`
	Error  string      `json:"error"`
	Limits []abosLimit `json:"limits"`
}

type abosWatcherState struct {
	GeneratedAt int64         `json:"generated_at"`
	Accounts    []abosAccount `json:"accounts"`
}

// oneapi-spend-guard status --json (P3)
type abosSpend struct {
	MonthEUR      float64 `json:"month_eur"`
	MonthLimitEUR float64 `json:"month_limit_eur"`
	DayEUR        float64 `json:"day_eur"`
	DayTripEUR    float64 `json:"day_tripwire_eur"`
	Guard         string  `json:"guard"`
}

type abosOut struct {
	ID         string        `json:"id"`
	Kind       string        `json:"kind"`
	Plan       string        `json:"plan,omitempty"`
	Feed       string        `json:"feed"`
	FeedStatus string        `json:"feed_status"` // ok | stale | fehlt | error
	FeedAgeS   int64         `json:"feed_age_s,omitempty"`
	Hint       string        `json:"hint,omitempty"`
	Ampel      string        `json:"ampel"` // ok | warn | crit | none
	Limits     []abosLimit   `json:"limits"`
	Bindings   []abosBinding `json:"bindings"`
	Spend      *abosSpend    `json:"spend,omitempty"`
}

// Registry-Cache: re-parse nur bei mtime-Wechsel (Muster planfile-adapter,
// stat statt fsnotify — kein Goroutine-Lifecycle nötig).
var (
	abosMu       sync.Mutex
	abosReg      abosRegistry
	abosRegMtime time.Time
	abosFeedAt   time.Time
	abosFeedOut  []abosOut
)

func abosRegistryPath() string { return envOr("ABOS_REGISTRY", "/opt/stack/tools/portfolio/resources.yaml") }
func abosStatePath() string {
	return envOr("ABOS_WATCHER_STATE", "/opt/claude-abo-watch/state/last.json")
}

func loadAbosRegistry() (abosRegistry, error) {
	st, err := os.Stat(abosRegistryPath())
	if err != nil {
		return abosRegistry{}, err
	}
	if st.ModTime().Equal(abosRegMtime) && len(abosReg.Resources) > 0 {
		return abosReg, nil
	}
	raw, err := os.ReadFile(abosRegistryPath())
	if err != nil {
		return abosRegistry{}, err
	}
	var reg abosRegistry
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		return abosRegistry{}, err
	}
	abosReg, abosRegMtime = reg, st.ModTime()
	return reg, nil
}

func abosAmpel(limits []abosLimit) string {
	if len(limits) == 0 {
		return "none"
	}
	out := "ok"
	for _, l := range limits {
		if l.Severity == "rejected" || l.Percent >= 95 {
			return "crit"
		}
		if l.Severity == "warning" || l.Percent >= 80 {
			out = "warn"
		}
	}
	return out
}

func abosSpendAmpel(s *abosSpend) string {
	if s == nil || s.MonthLimitEUR <= 0 {
		return "none"
	}
	pct := s.MonthEUR / s.MonthLimitEUR * 100
	switch {
	case s.Guard == "tripped" || pct >= 95:
		return "crit"
	case pct >= 80:
		return "warn"
	default:
		return "ok"
	}
}

func loadSpendGuard() (*abosSpend, string) {
	bin := envOr("ABOS_SPEND_GUARD", "/usr/local/bin/oneapi-spend-guard")
	out, err := exec.Command(bin, "status", "--json").Output()
	if err != nil {
		return nil, "error: spend-guard --json nicht verfügbar — P3 nachrüsten oder Pfad via ABOS_SPEND_GUARD setzen"
	}
	var s abosSpend
	if err := json.Unmarshal(out, &s); err != nil {
		return nil, "error: spend-guard-JSON nicht parsebar: " + err.Error()
	}
	return &s, ""
}

func buildAbos() []abosOut {
	reg, err := loadAbosRegistry()
	if err != nil {
		return []abosOut{{ID: "registry", Kind: "error", FeedStatus: "error", Ampel: "none",
			Hint: "resources.yaml nicht lesbar: " + err.Error(), Limits: []abosLimit{}, Bindings: []abosBinding{}}}
	}

	var watcher abosWatcherState
	watcherErr := ""
	if raw, err := os.ReadFile(abosStatePath()); err != nil {
		watcherErr = "claude-abo-watch state fehlt (" + err.Error() + ") — Timer prüfen: systemctl status claude-abo-watch.timer"
	} else if err := json.Unmarshal(raw, &watcher); err != nil {
		watcherErr = "claude-abo-watch state nicht parsebar: " + err.Error()
	}
	accounts := map[string]abosAccount{}
	for _, a := range watcher.Accounts {
		accounts[a.Name] = a
	}
	staleAfter := int64(3600)
	feedAge := int64(0)
	if watcher.GeneratedAt > 0 {
		feedAge = time.Now().Unix() - watcher.GeneratedAt
	}

	out := make([]abosOut, 0, len(reg.Resources))
	for _, res := range reg.Resources {
		o := abosOut{ID: res.ID, Kind: res.Kind, Plan: res.Plan, Feed: res.Feed,
			Limits: []abosLimit{}, Bindings: res.Bindings, Ampel: "none"}
		if o.Bindings == nil {
			o.Bindings = []abosBinding{}
		}
		switch {
		case res.Kind == "claude-abo" && res.Feed != "none" && res.Feed != "":
			o.FeedAgeS = feedAge
			acc, found := accounts[res.Feed]
			switch {
			case watcherErr != "":
				o.FeedStatus, o.Hint = "error", watcherErr
			case !found:
				o.FeedStatus = "error"
				o.Hint = "Account '" + res.Feed + "' fehlt in accounts.conf des Watchers"
			case !acc.OK:
				o.FeedStatus, o.Hint = "error", acc.Error
			case feedAge > staleAfter:
				o.FeedStatus = "stale"
				o.Limits, o.Ampel = acc.Limits, abosAmpel(acc.Limits)
			default:
				o.FeedStatus = "ok"
				o.Limits, o.Ampel = acc.Limits, abosAmpel(acc.Limits)
			}
		case res.Kind == "api-budget":
			spend, hint := loadSpendGuard()
			if spend == nil {
				o.FeedStatus, o.Hint = "error", hint
			} else {
				o.FeedStatus, o.Spend, o.Ampel = "ok", spend, abosSpendAmpel(spend)
			}
		default: // plan-ohne-feed oder feed: none
			o.FeedStatus = "fehlt"
			if res.Kind == "claude-abo" {
				o.Hint = "Token fehlt — setup-token minten, dann accounts.conf + resources.yaml nachziehen"
			}
		}
		out = append(out, o)
	}
	return out
}

func handleAbos(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	abosMu.Lock()
	if time.Since(abosFeedAt) > 60*time.Second || abosFeedOut == nil {
		abosFeedOut = buildAbos()
		abosFeedAt = time.Now()
	}
	res := abosFeedOut
	age := abosFeedAt
	abosMu.Unlock()
	json.NewEncoder(w).Encode(map[string]any{
		"resources":  res,
		"fetched_at": age.Unix(),
	})
}
