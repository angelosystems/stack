// Package broker is a minimal in-process secrets broker for tenant isolation.
//
// Design: secrets live only in the broker process memory (loaded at start from
// an out-of-image source — env, file mount, or upstream provider). Workspaces
// fetch their own tenant's secrets via an authenticated HTTP call and receive
// them as plaintext on the response, never persisted to disk by the broker.
// A small injector script (inject.sh) consumes the response and exports it as
// an env-var into the workspace process, satisfying the "no secrets on
// filesystem" acceptance criterion.
package broker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Policy is the per-tenant access policy. A tenant may only read the secret
// names listed under its own entry; cross-tenant access is rejected.
type Policy struct {
	Tenants map[string]TenantPolicy `yaml:"tenants"`
}

type TenantPolicy struct {
	Token   string   `yaml:"token"`   // shared-secret auth header value
	Secrets []string `yaml:"secrets"` // names the tenant may read
}

// Store holds tenant secret values in process memory. The map key is
// "<tenant>/<name>". Secrets are loaded from env-vars at start, never read
// from the container filesystem.
type Store struct {
	mu     sync.RWMutex
	values map[string]string
}

func NewStore() *Store { return &Store{values: map[string]string{}} }

func (s *Store) Set(tenant, name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[tenant+"/"+name] = value
}

func (s *Store) Get(tenant, name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[tenant+"/"+name]
	return v, ok
}

// LoadPolicy reads the YAML policy file. The policy file itself contains
// auth-tokens and secret *names*, never secret *values*.
func LoadPolicy(path string) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadSecretsFromEnv populates the store from env-vars named
// SECRET_<TENANT>_<NAME>. This keeps secret material out of the image and out
// of any on-disk config.
func (s *Store) LoadSecretsFromEnv(p *Policy) {
	for tenant, tp := range p.Tenants {
		for _, name := range tp.Secrets {
			key := fmt.Sprintf("SECRET_%s_%s", strings.ToUpper(tenant), strings.ToUpper(name))
			if v, ok := os.LookupEnv(key); ok {
				s.Set(tenant, name, v)
			}
		}
	}
}

// Handler returns an http.Handler exposing GET /v1/secret/{name}. The caller
// identifies itself via the X-Tenant header and authenticates with a bearer
// token in Authorization. Cross-tenant or unauthenticated requests get 403.
func Handler(p *Policy, s *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/secret/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/v1/secret/")
		if name == "" || strings.Contains(name, "/") {
			http.Error(w, "bad secret name", http.StatusBadRequest)
			return
		}
		tenant := r.Header.Get("X-Tenant")
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		tp, ok := p.Tenants[tenant]
		if !ok || auth == "" || auth != tp.Token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !contains(tp.Secrets, name) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		v, ok := s.Get(tenant, name)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": name, "value": v})
	})
	return mux
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
