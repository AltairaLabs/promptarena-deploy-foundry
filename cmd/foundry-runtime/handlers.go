package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Routes this container serves.
//
// /readiness is required of every hosted agent. Microsoft's Python and C#
// protocol libraries provide it; a Go container must implement it itself, and
// a version whose container never answers it stays stuck in `creating`.
const (
	routeReadiness   = "/readiness"
	routeInvocations = "/invocations"
)

// contentTypeSSE is the media type for the streaming response.
const contentTypeSSE = "text/event-stream"

// sseDoneEvent terminates a stream. Borrowed from the OpenAI convention so
// clients that already understand SSE need no special casing.
const sseDoneEvent = "[DONE]"

// invocationRequest is the request body for POST /invocations.
//
// The invocations contract is "arbitrary JSON in and out" — the platform
// relays it without inspecting the schema — so this shape is ours to define.
// Message has two aliases because callers arriving from other hyperscalers
// reasonably reach for `input` or `prompt`, and a silently empty turn is a
// miserable thing to debug.
type invocationRequest struct {
	Message string `json:"message"`
	Input   string `json:"input"`
	Prompt  string `json:"prompt"`
	Stream  bool   `json:"stream"`
	// ConversationID maps to a PromptKit session key. The platform manages its
	// own history for `responses`; here the pack stays authoritative and this
	// is transport only.
	ConversationID string `json:"conversation_id"`
}

// text returns the user's turn, whichever field carried it.
func (r *invocationRequest) text() string {
	for _, candidate := range []string{r.Message, r.Input, r.Prompt} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

// invocationResponse is the unary response body.
type invocationResponse struct {
	Output         string `json:"output"`
	ConversationID string `json:"conversation_id,omitempty"`
}

// sseDelta is one streamed text fragment.
type sseDelta struct {
	Delta string `json:"delta"`
}

// sseError reports a failure that arrived after the response had begun.
type sseError struct {
	Error string `json:"error"`
}

// turnFunc executes one conversation turn and returns the reply text.
type turnFunc func(ctx context.Context, req *invocationRequest) (string, error)

// streamFunc executes one streaming turn, returning a channel of text chunks
// and a channel carrying a terminal error. The text channel closes when the
// turn completes; the error channel yields at most one error.
type streamFunc func(ctx context.Context, req *invocationRequest) (<-chan string, <-chan error)

// decodeInvocation reads and validates the request body.
func decodeInvocation(r *http.Request) (*invocationRequest, error) {
	var req invocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	if req.text() == "" {
		return nil, fmt.Errorf("one of message, input or prompt is required")
	}
	return &req, nil
}

// newInvocationsHandler serves POST /invocations, unary or streaming.
func newInvocationsHandler(turn turnFunc, stream streamFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		req, err := decodeInvocation(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Stream {
			if stream == nil {
				// Refuse rather than quietly answering unary: a caller waiting
				// on deltas would otherwise hang until the whole turn finished.
				http.Error(w, "streaming is not available", http.StatusNotImplemented)
				return
			}
			chunks, errs := stream(r.Context(), req)
			writeSSE(w, chunks, errs)
			return
		}

		out, err := turn(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(invocationResponse{
			Output:         out,
			ConversationID: req.ConversationID,
		})
	})
}

// writeSSE drains the chunk channel to the response as server-sent events.
//
// An error arriving before the first chunk becomes an HTTP status; after that
// the status is already on the wire, so it has to travel as an event instead.
func writeSSE(w http.ResponseWriter, chunks <-chan string, errs <-chan error) {
	stream := &sseWriter{w: w}
	stream.flusher, stream.canFlush = w.(http.Flusher)

	for text := range chunks {
		stream.ensureHeader()
		if !stream.event(sseDelta{Delta: text}) {
			return
		}
	}

	err := <-errs
	// Before any output the status line is still ours to choose; after it, the
	// failure has to travel in-band as an event.
	if err != nil && !stream.wroteHeader {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	stream.ensureHeader()
	if err != nil {
		stream.event(sseError{Error: err.Error()})
	}
	stream.done()
}

// sseWriter emits server-sent events, writing the headers lazily so an early
// failure can still choose an HTTP status.
type sseWriter struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	canFlush    bool
	wroteHeader bool
}

// ensureHeader writes the streaming headers once.
func (s *sseWriter) ensureHeader() {
	if s.wroteHeader {
		return
	}
	writeSSEHeader(s.w)
	s.wroteHeader = true
}

// event encodes one payload as an SSE data frame, reporting whether it landed.
func (s *sseWriter) event(payload any) bool {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", encoded); err != nil {
		return false
	}
	s.flush()
	return true
}

// done writes the stream terminator.
func (s *sseWriter) done() {
	_, _ = fmt.Fprintf(s.w, "data: %s\n\n", sseDoneEvent)
	s.flush()
}

// flush pushes buffered bytes to the client so deltas arrive as they are
// produced rather than at the end of the turn.
func (s *sseWriter) flush() {
	if s.canFlush {
		s.flusher.Flush()
	}
}

// writeSSEHeader sends the streaming response headers.
func writeSSEHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", contentTypeSSE)
	// The platform relays these frames; buffering anywhere in between would
	// defeat the point of streaming at all.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}
