package broker

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPolicy() *Policy {
	return &Policy{Tenants: map[string]TenantPolicy{
		"stayawesome":  {Token: "saw-tok", Secrets: []string{"gmail_oauth"}},
		"quantumshift": {Token: "qs-tok", Secrets: []string{"binance_api"}},
	}}
}

func testStore() *Store {
	s := NewStore()
	s.Set("stayawesome", "gmail_oauth", "saw-value")
	s.Set("quantumshift", "binance_api", "qs-value")
	return s
}

// Acceptance #1: a workspace reads its own tenant's secret.
func TestOwnTenantCanRead(t *testing.T) {
	srv := httptest.NewServer(Handler(testPolicy(), testStore()))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/secret/gmail_oauth", nil)
	req.Header.Set("X-Tenant", "stayawesome")
	req.Header.Set("Authorization", "Bearer saw-tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["value"] != "saw-value" {
		t.Fatalf("want saw-value, got %q", body["value"])
	}
}

// Acceptance #2: cross-tenant access fails (3 vectors).
func TestCrossTenantAccessForbidden(t *testing.T) {
	srv := httptest.NewServer(Handler(testPolicy(), testStore()))
	defer srv.Close()

	cases := []struct {
		name, tenant, token, secret string
		wantStatus                   int
	}{
		{"wrong-token-for-own-tenant", "stayawesome", "qs-tok", "gmail_oauth", 403},
		{"own-token-for-other-tenant-secret", "stayawesome", "saw-tok", "binance_api", 403},
		{"impersonate-other-tenant-no-token", "quantumshift", "", "binance_api", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", srv.URL+"/v1/secret/"+c.secret, nil)
			req.Header.Set("X-Tenant", c.tenant)
			if c.token != "" {
				req.Header.Set("Authorization", "Bearer "+c.token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("want %d, got %d", c.wantStatus, resp.StatusCode)
			}
		})
	}
}

// Acceptance #3: no secret *values* live on disk. The policy file holds names
// and auth tokens only; secret values are sourced from env-vars at runtime.
func TestNoSecretValuesOnDisk(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(policyPath, []byte(`tenants:
  stayawesome:
    token: saw-tok
    secrets: [gmail_oauth]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(policyPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("SECRET_STAYAWESOME_GMAIL_OAUTH", "live-value-from-env")
	s := NewStore()
	s.LoadSecretsFromEnv(p)

	// Sanity: value is reachable in memory.
	if v, _ := s.Get("stayawesome", "gmail_oauth"); v != "live-value-from-env" {
		t.Fatalf("want value loaded from env, got %q", v)
	}

	// Walk the simulated image/filesystem rooted at dir and confirm the
	// secret value never appears in any file. The policy file is allowed to
	// contain only metadata.
	needle := "live-value-from-env"
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), needle) {
			t.Fatalf("secret value leaked to %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
