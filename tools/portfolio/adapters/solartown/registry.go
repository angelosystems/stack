package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Rig struct {
	Prefix string `json:"prefix"`
	Dir    string `json:"dir"`
	DSN    string `json:"dsn"`
}

type Registry struct {
	rigs map[string]Rig
}

func LoadRegistry(configStr string) (*Registry, error) {
	rigs := make(map[string]Rig)

	addRig := func(r Rig) error {
		r.Prefix = strings.TrimSpace(r.Prefix)
		r.Dir = strings.TrimSpace(r.Dir)
		r.DSN = strings.TrimSpace(r.DSN)
		if r.Prefix == "" {
			return nil
		}
		if _, exists := rigs[r.Prefix]; exists {
			return fmt.Errorf("duplicate prefix detected: %s", r.Prefix)
		}
		rigs[r.Prefix] = r
		return nil
	}

	configStr = strings.TrimSpace(configStr)
	if configStr == "" {
		// Load standard default rigs
		defaults := []Rig{
			{Prefix: "st", Dir: "/opt/solartown", DSN: "postgres://remote:remote@127.0.0.1:5433/solartown_clean?sslmode=disable"},
			{Prefix: "tr", Dir: "/opt/solartown", DSN: "postgres://remote:remote@127.0.0.1:5433/solartown_clean?sslmode=disable"},
			{Prefix: "qu", Dir: "/opt/quantbot", DSN: "postgres://remote:remote@127.0.0.1:5433/quantbot_clean?sslmode=disable"},
			{Prefix: "sk", Dir: "/opt/stack", DSN: "postgres://remote:remote@127.0.0.1:5433/stack_clean?sslmode=disable"},
			{Prefix: "so", Dir: "/root/stayawesomeOS", DSN: "postgres://remote:remote@127.0.0.1:5433/stayawesome_clean?sslmode=disable"},
			{Prefix: "sa", Dir: "/root/stayawesomeOS", DSN: "postgres://remote:remote@127.0.0.1:5433/stayawesome_clean?sslmode=disable"},
			{Prefix: "cl", Dir: "/opt/angeloos", DSN: "postgres://remote:remote@127.0.0.1:5433/angeloos_clean?sslmode=disable"},
			{Prefix: "ag", Dir: "/opt/angeloos", DSN: "postgres://remote:remote@127.0.0.1:5433/angeloos_clean?sslmode=disable"},
			{Prefix: "mb", Dir: "/root/solartown/mariobrain", DSN: "postgres://remote:remote@127.0.0.1:5433/mariobrain_clean?sslmode=disable"},
		}
		for _, r := range defaults {
			if err := addRig(r); err != nil {
				return nil, err
			}
		}
		return &Registry{rigs: rigs}, nil
	}

	// Try parsing as JSON first
	if strings.HasPrefix(configStr, "[") || strings.HasPrefix(configStr, "{") {
		var list []Rig
		if strings.HasPrefix(configStr, "{") {
			var obj map[string]struct {
				Dir string `json:"dir"`
				DSN string `json:"dsn"`
			}
			if err := json.Unmarshal([]byte(configStr), &obj); err != nil {
				return nil, fmt.Errorf("failed to parse JSON object: %w", err)
			}
			for k, v := range obj {
				list = append(list, Rig{Prefix: k, Dir: v.Dir, DSN: v.DSN})
			}
		} else {
			if err := json.Unmarshal([]byte(configStr), &list); err != nil {
				return nil, fmt.Errorf("failed to parse JSON array: %w", err)
			}
		}
		for _, r := range list {
			if err := addRig(r); err != nil {
				return nil, err
			}
		}
		return &Registry{rigs: rigs}, nil
	}

	// Parse custom string format prefix=dir=dsn separated by newlines or semicolons
	var entries []string
	if strings.Contains(configStr, "\n") {
		entries = strings.Split(configStr, "\n")
	} else if strings.Contains(configStr, ";") {
		entries = strings.Split(configStr, ";")
	} else {
		entries = []string{configStr}
	}

	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 3)
		if len(parts) != 3 {
			colParts := strings.SplitN(entry, ":", 3)
			if len(colParts) == 3 && !strings.Contains(colParts[0], "=") {
				parts = colParts
			} else {
				return nil, fmt.Errorf("invalid entry format: %s (expected prefix=dir=dsn)", entry)
			}
		}
		r := Rig{
			Prefix: parts[0],
			Dir:    parts[1],
			DSN:    parts[2],
		}
		if err := addRig(r); err != nil {
			return nil, err
		}
	}

	return &Registry{rigs: rigs}, nil
}

func (reg *Registry) Get(prefix string) (Rig, bool) {
	r, ok := reg.rigs[prefix]
	return r, ok
}

func (reg *Registry) Resolve(id string) (Rig, bool) {
	parts := strings.Split(id, "-")
	if len(parts) > 0 {
		return reg.Get(parts[0])
	}
	return Rig{}, false
}

var reg *Registry

func initRegistry() {
	var err error
	reg, err = LoadRegistry(os.Getenv("RIG_REGISTRY"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to load Rig-Registry:", err)
		os.Exit(1)
	}
}
