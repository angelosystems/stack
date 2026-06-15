package main

import (
	"strings"
	"testing"
)

func TestLoadRegistry_Success(t *testing.T) {
	config := "qu=/opt/quantbot=postgres://quant;st=/opt/solartown=postgres://solar"
	reg, err := LoadRegistry(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rig, ok := reg.Get("qu")
	if !ok {
		t.Fatalf("expected prefix 'qu' to be registered")
	}
	if rig.Dir != "/opt/quantbot" || rig.DSN != "postgres://quant" {
		t.Errorf("unexpected rig fields: %+v", rig)
	}

	rig, ok = reg.Get("st")
	if !ok {
		t.Fatalf("expected prefix 'st' to be registered")
	}
	if rig.Dir != "/opt/solartown" || rig.DSN != "postgres://solar" {
		t.Errorf("unexpected rig fields: %+v", rig)
	}

	// Test Resolve helper
	resolved, ok := reg.Resolve("qu-123")
	if !ok || resolved.Prefix != "qu" {
		t.Errorf("failed to resolve bead qu-123 correctly: ok=%v, prefix=%s", ok, resolved.Prefix)
	}
}

func TestLoadRegistry_JSON_Success(t *testing.T) {
	config := `[
		{"prefix": "qu", "dir": "/opt/quantbot", "dsn": "postgres://quant"},
		{"prefix": "st", "dir": "/opt/solartown", "dsn": "postgres://solar"}
	]`
	reg, err := LoadRegistry(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := reg.Get("qu"); !ok {
		t.Errorf("expected 'qu' registered")
	}
	if _, ok := reg.Get("st"); !ok {
		t.Errorf("expected 'st' registered")
	}
}

func TestLoadRegistry_JSONObject_Success(t *testing.T) {
	config := `{
		"qu": {"dir": "/opt/quantbot", "dsn": "postgres://quant"},
		"st": {"dir": "/opt/solartown", "dsn": "postgres://solar"}
	}`
	reg, err := LoadRegistry(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := reg.Get("qu"); !ok {
		t.Errorf("expected 'qu' registered")
	}
	if _, ok := reg.Get("st"); !ok {
		t.Errorf("expected 'st' registered")
	}
}

func TestLoadRegistry_Duplicate_Failure(t *testing.T) {
	config := "qu=/opt/quantbot1=postgres://quant1;qu=/opt/quantbot2=postgres://quant2"
	_, err := LoadRegistry(config)
	if err == nil {
		t.Fatalf("expected error on duplicate prefix, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate prefix detected") {
		t.Errorf("expected duplicate prefix error message, got: %v", err)
	}
}

func TestLoadRegistry_JSON_Duplicate_Failure(t *testing.T) {
	config := `[
		{"prefix": "qu", "dir": "/opt/quantbot", "dsn": "postgres://quant"},
		{"prefix": "qu", "dir": "/opt/other", "dsn": "postgres://other"}
	]`
	_, err := LoadRegistry(config)
	if err == nil {
		t.Fatalf("expected error on JSON duplicate prefix, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate prefix detected") {
		t.Errorf("expected duplicate prefix error message, got: %v", err)
	}
}

func TestLoadRegistry_Defaults(t *testing.T) {
	reg, err := LoadRegistry("")
	if err != nil {
		t.Fatalf("unexpected error on empty config: %v", err)
	}

	if _, ok := reg.Get("st"); !ok {
		t.Errorf("expected default 'st' registered")
	}
	if _, ok := reg.Get("qu"); !ok {
		t.Errorf("expected default 'qu' registered")
	}
}

func TestGetUniqueDSNs(t *testing.T) {
	// 1. With nil registry
	dsns := getUniqueDSNs(nil, "postgres://fallback")
	if len(dsns) != 1 || dsns[0] != "postgres://fallback" {
		t.Errorf("expected fallback DSN, got: %v", dsns)
	}

	// 2. With duplicate DSNs in registry
	config := "qu=/opt/quantbot=postgres://shared;st=/opt/solartown=postgres://shared;other=/opt/other=postgres://unique"
	registry, err := LoadRegistry(config)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}

	dsns = getUniqueDSNs(registry, "postgres://fallback")
	if len(dsns) != 2 {
		t.Errorf("expected 2 unique DSNs, got: %d (%v)", len(dsns), dsns)
	}

	hasShared := false
	hasUnique := false
	for _, d := range dsns {
		if d == "postgres://shared" {
			hasShared = true
		}
		if d == "postgres://unique" {
			hasUnique = true
		}
	}
	if !hasShared || !hasUnique {
		t.Errorf("missing expected DSNs: %v", dsns)
	}

	// 3. With empty DSN
	configEmpty := "qu=/opt/quantbot=;st=/opt/solartown="
	registryEmpty, err := LoadRegistry(configEmpty)
	if err != nil {
		t.Fatalf("failed to load registry: %v", err)
	}
	dsns = getUniqueDSNs(registryEmpty, "postgres://fallback")
	if len(dsns) != 1 || dsns[0] != "postgres://fallback" {
		t.Errorf("expected fallback DSN when registry has only empty DSNs, got: %v", dsns)
	}
}
