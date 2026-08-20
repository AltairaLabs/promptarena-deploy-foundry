package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/providers"
	"github.com/gorilla/websocket"
)

// fakeConversation records what the relay sent it and replays canned output.
type fakeConversation struct {
	mu      sync.Mutex
	audio   [][]byte
	texts   []string
	closed  bool
	sendErr error
	respCh  chan providers.StreamChunk
	respErr error
}

func newFakeConversation() *fakeConversation {
	return &fakeConversation{respCh: make(chan providers.StreamChunk, 8)}
}

func (f *fakeConversation) SendChunk(_ context.Context, chunk *providers.StreamChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	if chunk.MediaData != nil {
		f.audio = append(f.audio, chunk.MediaData.Data)
	}
	return nil
}

func (f *fakeConversation) SendText(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	return nil
}

func (f *fakeConversation) Response() (<-chan providers.StreamChunk, error) {
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.respCh, nil
}

func (f *fakeConversation) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConversation) audioFrames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.audio...)
}

func (f *fakeConversation) textsSent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// voiceServer starts the handler over a real WebSocket and dials it.
func voiceServer(t *testing.T, conv duplexConversation, openErr error) *websocket.Conn {
	t.Helper()

	deps := voiceDeps{
		Open: func() (duplexConversation, error) {
			if openErr != nil {
				return nil, openErr
			}
			return conv, nil
		},
		Log: discardLogger(),
	}
	handler := newVoiceHandler(deps, &websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_ = resp.Body.Close()
	return client
}

// readEvent reads one text frame as an event.
func readEvent(t *testing.T, c *websocket.Conn) outboundEvent {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		msgType, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if msgType != websocket.TextMessage {
			continue
		}
		var ev outboundEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("event is not valid JSON: %v", err)
		}
		return ev
	}
}

// The client is told the session is live before any audio is expected of it.
func TestVoiceSendsReadyOnConnect(t *testing.T) {
	client := voiceServer(t, newFakeConversation(), nil)

	if ev := readEvent(t, client); ev.Type != eventReady {
		t.Errorf("first event = %q, want %q", ev.Type, eventReady)
	}
}

// Binary frames are audio: they must reach the pipeline as media chunks, not
// as text, or the turn is silently empty.
func TestVoiceRelaysBinaryFramesAsAudio(t *testing.T) {
	conv := newFakeConversation()
	client := voiceServer(t, conv, nil)
	readEvent(t, client) // ready

	frame := make([]byte, 640)
	frame[0] = 0x7f
	if err := client.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("write: %v", err)
	}

	waitFor(t, func() bool { return len(conv.audioFrames()) == 1 })

	got := conv.audioFrames()[0]
	if len(got) != 640 || got[0] != 0x7f {
		t.Errorf("relayed %d bytes starting %#x, want the frame verbatim", len(got), got[0])
	}
}

// Synthesized audio must come back as binary, so a client can play it without
// decoding an envelope.
func TestVoiceRelaysSynthesizedAudioAsBinary(t *testing.T) {
	conv := newFakeConversation()
	client := voiceServer(t, conv, nil)
	readEvent(t, client) // ready

	conv.respCh <- providers.StreamChunk{
		MediaData: &providers.StreamMediaData{Data: []byte{1, 2, 3}, MIMEType: audioMIMEType},
	}

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		msgType, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if msgType == websocket.BinaryMessage {
			if len(data) != 3 {
				t.Errorf("audio frame = %d bytes, want 3", len(data))
			}
			return
		}
	}
}

func TestVoiceRelaysTextAndTranscript(t *testing.T) {
	conv := newFakeConversation()
	client := voiceServer(t, conv, nil)
	readEvent(t, client) // ready

	conv.respCh <- providers.StreamChunk{Delta: "Paris"}
	if ev := readEvent(t, client); ev.Type != eventText || ev.Text != "Paris" {
		t.Errorf("event = %+v, want a text delta", ev)
	}

	conv.respCh <- providers.StreamChunk{
		Metadata: map[string]any{"input_transcription": "what is the capital"},
	}
	if ev := readEvent(t, client); ev.Type != eventTranscript {
		t.Errorf("event = %+v, want a transcript", ev)
	}
}

// Barge-in: an explicit interrupt must reach the conversation, for clients that
// know the user has started talking before the audio proves it.
func TestVoiceControlInterrupt(t *testing.T) {
	conv := newFakeConversation()
	client := voiceServer(t, conv, nil)
	readEvent(t, client) // ready

	if err := client.WriteMessage(websocket.TextMessage,
		[]byte(`{"type":"interrupt"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	waitFor(t, func() bool { return len(conv.textsSent()) == 1 })
}

// A frame this runtime does not understand must not cost the caller its call.
func TestVoiceIgnoresUnknownAndMalformedControl(t *testing.T) {
	conv := newFakeConversation()
	client := voiceServer(t, conv, nil)
	readEvent(t, client) // ready

	for _, frame := range []string{`{`, `{"type":"nonsense"}`} {
		if err := client.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
			t.Fatalf("write %q: %v", frame, err)
		}
	}

	// The socket must still carry audio afterwards.
	if err := client.WriteMessage(websocket.BinaryMessage, make([]byte, 640)); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	waitFor(t, func() bool { return len(conv.audioFrames()) == 1 })

	if sent := conv.textsSent(); len(sent) != 0 {
		t.Errorf("unknown control frames produced %v, want none", sent)
	}
}

// The client must learn the turn is over rather than waiting on a silent socket.
func TestVoiceSendsDoneWhenTheStreamCloses(t *testing.T) {
	conv := newFakeConversation()
	client := voiceServer(t, conv, nil)
	readEvent(t, client) // ready

	close(conv.respCh)

	if ev := readEvent(t, client); ev.Type != eventDone {
		t.Errorf("event = %+v, want %q", ev, eventDone)
	}
}

// A conversation that cannot start must say so, not leave the client waiting
// for a ready that never comes.
func TestVoiceReportsOpenFailure(t *testing.T) {
	client := voiceServer(t, nil, errors.New("no model"))

	ev := readEvent(t, client)
	if ev.Type != eventError {
		t.Fatalf("event = %+v, want an error", ev)
	}
	if !strings.Contains(ev.Error, "no model") {
		t.Errorf("Error = %q, want the underlying reason", ev.Error)
	}
}

func TestVoiceReportsResponseStreamFailure(t *testing.T) {
	conv := newFakeConversation()
	conv.respErr = errors.New("stream unavailable")
	client := voiceServer(t, conv, nil)

	if ev := readEvent(t, client); ev.Type != eventError {
		t.Errorf("event = %+v, want an error", ev)
	}
}

// A send failure ends the call rather than silently dropping the caller's audio.
func TestVoiceReportsAudioSendFailure(t *testing.T) {
	conv := newFakeConversation()
	conv.sendErr = errors.New("pipeline gone")
	client := voiceServer(t, conv, nil)
	readEvent(t, client) // ready

	if err := client.WriteMessage(websocket.BinaryMessage, make([]byte, 640)); err != nil {
		t.Fatalf("write: %v", err)
	}

	if ev := readEvent(t, client); ev.Type != eventError {
		t.Errorf("event = %+v, want an error", ev)
	}
}

// waitFor polls until cond holds, failing the test if it never does.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
