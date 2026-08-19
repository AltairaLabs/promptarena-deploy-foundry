package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListenAddr(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"injected PORT wins", map[string]string{"PORT": "9000"}, "0.0.0.0:9000"},
		{"falls back to the documented default", nil, "0.0.0.0:8088"},
		{"empty PORT falls back", map[string]string{"PORT": ""}, "0.0.0.0:8088"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := listenAddr(func(k string) string { return tt.env[k] })
			if got != tt.want {
				t.Errorf("listenAddr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadiness(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readiness", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET /readiness = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestNewServerHasReadHeaderTimeout(t *testing.T) {
	// Without this the server accepts slow-header connections indefinitely, and
	// gosec flags it. Assert it rather than trusting the literal.
	if got := newServer().ReadHeaderTimeout; got != readHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", got, readHeaderTimeout)
	}
}
