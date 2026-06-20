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

	mb, ok := reg.Get("mb")
	if !ok {
		t.Fatalf("expected default 'mb' registered")
	}
	if mb.Dir != "/root/mario-brain" {
		t.Errorf("mb dir should be /root/mario-brain (corrected from /root/solartown/mariobrain), got %q", mb.Dir)
	}
}
