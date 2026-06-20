package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCheckAuth(t *testing.T) {
	// Setup test environment
	os.Setenv("PORTFOLIO_API_KEY", "test-secret-key")
	defer os.Unsetenv("PORTFOLIO_API_KEY")

	tests := []struct {
		name           string
		headers        map[string]string
		expectedResult bool
	}{
		{
			name:           "No authentication headers",
			headers:        map[string]string{},
			expectedResult: false,
		},
		{
			name: "SSO email authentication header present",
			headers: map[string]string{
				"X-Auth-Request-Email": "testuser@stayawesome.de",
			},
			expectedResult: true,
		},
		{
			name: "Valid API-Key authentication header present",
			headers: map[string]string{
				"X-Api-Key": "test-secret-key",
			},
			expectedResult: true,
		},
		{
			name: "Invalid API-Key authentication header present",
			headers: map[string]string{
				"X-Api-Key": "wrong-secret-key",
			},
			expectedResult: false,
		},
		{
			name: "Both SSO and valid API-Key present",
			headers: map[string]string{
				"X-Auth-Request-Email": "testuser@stayawesome.de",
				"X-Api-Key":            "test-secret-key",
			},
			expectedResult: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest("POST", "/api/dispatch", nil)
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}

			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			result := checkAuth(req)
			if result != tc.expectedResult {
				t.Errorf("expected checkAuth to return %t, got %t", tc.expectedResult, result)
			}
		})
	}
}

func TestAuthMiddlewareRejection(t *testing.T) {
	os.Setenv("PORTFOLIO_API_KEY", "test-secret-key")
	defer os.Unsetenv("PORTFOLIO_API_KEY")

	// We create a dummy handler that requires checkAuth
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	server := httptest.NewServer(dummyHandler)
	defer server.Close()

	// 1. Test unauthorized request
	resp, err := http.Post(server.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, resp.StatusCode)
	}

	// 2. Test authorized request via SSO header
	req, err := http.NewRequest("POST", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("X-Auth-Request-Email", "mario@stayawesome.de")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp2.StatusCode)
	}

	// 3. Test authorized request via API Key header
	req3, err := http.NewRequest("POST", server.URL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req3.Header.Set("X-Api-Key", "test-secret-key")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp3.StatusCode)
	}
}
