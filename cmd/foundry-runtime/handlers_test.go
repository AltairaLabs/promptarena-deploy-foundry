package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func okTurn(_ context.Context, req *invocationRequest) (string, error) {
	return "echo: " + req.Message, nil
}

func failTurn(_ context.Context, _ *invocationRequest) (string, error) {
	return "", errors.New("model unavailable")
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/invocations", strings.NewReader(body)))
	return rec
}

func TestInvocationsUnary(t *testing.T) {
	rec := post(t, newInvocationsHandler(okTurn, nil), `{"message":"hello"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got invocationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if got.Output != "echo: hello" {
		t.Errorf("Output = %q, want %q", got.Output, "echo: hello")
	}
}

func TestInvocationsRejectsNonPost(t *testing.T) {
	rec := httptest.NewRecorder()
	newInvocationsHandler(okTurn, nil).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/invocations", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestInvocationsRejectsMalformedBody(t *testing.T) {
	rec := post(t, newInvocationsHandler(okTurn, nil), `{`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// An empty message has nothing to send to the model, and a 400 tells the
// caller that far more usefully than a model error would.
func TestInvocationsRejectsEmptyMessage(t *testing.T) {
	rec := post(t, newInvocationsHandler(okTurn, nil), `{"message":""}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestInvocationsTurnFailure(t *testing.T) {
	rec := post(t, newInvocationsHandler(failTurn, nil), `{"message":"hello"}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(rec.Body.String(), "model unavailable") {
		t.Errorf("body = %q, want the underlying reason", rec.Body.String())
	}
}

// Accepts alternative field names so callers used to other hyperscalers'
// conventions are not caught out by a silent empty turn.
func TestInvocationsAcceptsAlternativeMessageFields(t *testing.T) {
	for _, field := range []string{"message", "input", "prompt"} {
		t.Run(field, func(t *testing.T) {
			rec := post(t, newInvocationsHandler(okTurn, nil), `{"`+field+`":"hi"}`)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d for field %q, body = %s", rec.Code, field, rec.Body.String())
			}
		})
	}
}

func streamOK(_ context.Context, _ *invocationRequest) (<-chan string, <-chan error) {
	out := make(chan string, 2)
	errCh := make(chan error, 1)
	out <- "Hel"
	out <- "lo"
	close(out)
	close(errCh)
	return out, errCh
}

func streamFails(_ context.Context, _ *invocationRequest) (<-chan string, <-chan error) {
	out := make(chan string)
	errCh := make(chan error, 1)
	close(out)
	errCh <- errors.New("stream broke")
	close(errCh)
	return out, errCh
}

// sseEvents collects the data payloads of an SSE response.
func sseEvents(t *testing.T, body string) []string {
	t.Helper()
	var events []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if after, found := strings.CutPrefix(line, "data: "); found {
			events = append(events, after)
		}
	}
	return events
}

func TestInvocationsStreamsSSE(t *testing.T) {
	rec := post(t, newInvocationsHandler(okTurn, streamOK), `{"message":"hi","stream":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, contentTypeSSE) {
		t.Errorf("Content-Type = %q, want %q", ct, contentTypeSSE)
	}

	events := sseEvents(t, rec.Body.String())
	if len(events) < 3 {
		t.Fatalf("events = %v, want two deltas and a terminator", events)
	}

	var first struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal([]byte(events[0]), &first); err != nil {
		t.Fatalf("first event is not valid JSON: %v", err)
	}
	if first.Delta != "Hel" {
		t.Errorf("first delta = %q, want Hel", first.Delta)
	}

	if events[len(events)-1] != sseDoneEvent {
		t.Errorf("last event = %q, want %q", events[len(events)-1], sseDoneEvent)
	}
}

// A stream that fails before any output can still use an HTTP status; once
// bytes are on the wire it cannot, so the error has to travel as an event.
func TestInvocationsStreamFailureBeforeOutput(t *testing.T) {
	rec := post(t, newInvocationsHandler(okTurn, streamFails), `{"message":"hi","stream":true}`)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestInvocationsStreamFailureAfterOutput(t *testing.T) {
	partial := func(_ context.Context, _ *invocationRequest) (<-chan string, <-chan error) {
		out := make(chan string, 1)
		errCh := make(chan error, 1)
		out <- "partial"
		close(out)
		errCh <- errors.New("died midway")
		close(errCh)
		return out, errCh
	}

	rec := post(t, newInvocationsHandler(okTurn, partial), `{"message":"hi","stream":true}`)

	// The status is already 200 by then, so the failure must surface in-band.
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d once bytes are written", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "died midway") {
		t.Errorf("body = %q, want the error reported as an event", rec.Body.String())
	}
}

// Streaming must be refused rather than silently answered unary, or a caller
// waiting on deltas would hang until the whole turn completed.
func TestInvocationsStreamWithoutAStreamer(t *testing.T) {
	rec := post(t, newInvocationsHandler(okTurn, nil), `{"message":"hi","stream":true}`)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

func TestReadiness(t *testing.T) {
	rec := httptest.NewRecorder()
	buildMux(okTurn, streamOK, nil).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, routeReadiness, nil))

	if rec.Code != http.StatusOK {
		t.Errorf("GET %s = %d, want %d", routeReadiness, rec.Code, http.StatusOK)
	}
}

func TestMuxServesInvocations(t *testing.T) {
	rec := post(t, buildMux(okTurn, streamOK, nil), `{"message":"hello"}`)

	if rec.Code != http.StatusOK {
		t.Errorf("POST %s = %d, want %d", routeInvocations, rec.Code, http.StatusOK)
	}
}

// Foundry's own protocol libraries answer /readiness with a JSON body, not a
// bare 200. Reverse-engineered from azure-ai-agentserver-core, which returns
// {"status":"healthy"} as application/json.
func TestReadinessReturnsHealthyJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	buildMux(okTurn, streamOK, nil).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, routeReadiness, nil))

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v (%q)", err, rec.Body.String())
	}
	if body.Status != "healthy" {
		t.Errorf("status = %q, want healthy", body.Status)
	}
}

// Every response from Foundry's own servers carries x-platform-server, added
// by middleware. It is the only thing resembling a container handshake in the
// protocol libraries, so a hand-rolled container must send it too.
func TestEveryResponseCarriesPlatformServerHeader(t *testing.T) {
	tests := []struct {
		name string
		req  *http.Request
	}{
		{"readiness", httptest.NewRequest(http.MethodGet, routeReadiness, nil)},
		{"invocations", httptest.NewRequest(http.MethodPost, routeInvocations,
			strings.NewReader(`{"message":"hi"}`))},
		{"bad request", httptest.NewRequest(http.MethodPost, routeInvocations,
			strings.NewReader(`{`))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			buildMux(okTurn, streamOK, nil).ServeHTTP(rec, tt.req)

			got := rec.Header().Get(headerPlatformServer)
			if got == "" {
				t.Fatalf("%s is missing", headerPlatformServer)
			}
			if !strings.Contains(got, "go/") {
				t.Errorf("%s = %q, want it to name the runtime", headerPlatformServer, got)
			}
		})
	}
}

// A text-only pack must not advertise a socket it cannot serve.
func TestMuxOmitsVoiceRouteWhenAbsent(t *testing.T) {
	rec := httptest.NewRecorder()
	buildMux(okTurn, streamOK, nil).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, routeInvocationsWS, nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET %s = %d, want %d", routeInvocationsWS, rec.Code, http.StatusNotFound)
	}
}

func TestMuxServesVoiceRouteWhenPresent(t *testing.T) {
	marker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	rec := httptest.NewRecorder()
	buildMux(okTurn, streamOK, marker).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, routeInvocationsWS, nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("GET %s = %d, want the voice handler to serve it", routeInvocationsWS, rec.Code)
	}
}

// testLogger returns a logger that discards output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
