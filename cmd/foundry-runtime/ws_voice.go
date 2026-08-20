package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/AltairaLabs/PromptKit/runtime/providers"
	"github.com/AltairaLabs/PromptKit/sdk"
	"github.com/gorilla/websocket"
)

// Audio framing on the wire. Verified against a live relay: a 640-byte binary
// frame — 20 ms of PCM at 16 kHz mono — arrives at the container intact, and
// the platform forwards text and binary frames verbatim in both directions.
const (
	audioSampleRate = 16000
	audioChannels   = 1
	audioMIMEType   = "audio/pcm"
)

// wsCloseInternalError is the close code the platform maps unhandled handler
// errors to, so the container uses it for its own failures too.
const wsCloseInternalError = websocket.CloseInternalServerErr

// wsWriteTimeout bounds a single frame write. The relay keeps the connection
// alive with its own 30 s Ping/Pong, so only individual writes are bounded.
const wsWriteTimeout = 10 * time.Second

// controlFrame is the JSON envelope carried by text frames.
//
// The invocations_ws relay forwards frames without interpreting them, so this
// schema is the runtime's own. Text carries control, binary carries audio.
type controlFrame struct {
	Type string `json:"type"`
}

// Control frame types.
const (
	// controlInterrupt is barge-in signaled by the client. VAD-detected
	// speech also interrupts, inside the pipeline; this is for clients that
	// know the user has started talking before the audio proves it.
	controlInterrupt = "interrupt"
)

// outboundEvent is a text frame sent to the client alongside the audio.
type outboundEvent struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// Outbound event types.
const (
	eventReady      = "ready"
	eventTranscript = "transcript"
	eventText       = "text"
	eventError      = "error"
	eventDone       = "done"
)

// duplexConversation is the slice of sdk.Conversation the relay actually uses.
// Naming it lets a test drive the handler without a model, an STT service or a
// network.
type duplexConversation interface {
	SendChunk(ctx context.Context, chunk *providers.StreamChunk) error
	SendText(ctx context.Context, text string) error
	Response() (<-chan providers.StreamChunk, error)
	Close() error
}

// voiceDeps is everything the WebSocket handler needs to open a turn.
type voiceDeps struct {
	// Open starts a duplex conversation. Tests substitute it; when nil the
	// real sdk.OpenDuplex is used.
	Open      func() (duplexConversation, error)
	PackFile  string
	AgentName string
	Opts      []sdk.Option
	Log       *slog.Logger
}

// openConversation starts a duplex conversation, honoring a test override.
func (d voiceDeps) openConversation() (duplexConversation, error) {
	if d.Open != nil {
		return d.Open()
	}
	return sdk.OpenDuplex(d.PackFile, d.AgentName, d.Opts...)
}

// newVoiceHandler serves WS /invocations_ws, cascading audio through the pack's
// own pipeline.
//
// The pipeline owns the input side: PromptKit's VAD decides when a turn ends,
// buffers jitter, and handles barge-in, and the model only ever sees text. So
// this handler is a relay between WebSocket frames and the duplex conversation,
// and a pack behaves identically over voice and text.
func newVoiceHandler(deps voiceDeps, upgrader *websocket.Upgrader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			// Upgrade already wrote a response.
			deps.Log.Error("websocket upgrade failed", "error", err)
			return
		}
		defer func() { _ = conn.Close() }()

		session := &voiceSession{conn: conn, deps: deps}
		session.run(r.Context())
	})
}

// voiceSession is one WebSocket conversation.
//
// Writes are serialized: the response pump runs on its own goroutine while the
// inbound loop can also write — an error event, a close frame — and a
// websocket connection supports only one concurrent writer. Without the mutex
// the race detector catches it, and in production it would corrupt frames.
type voiceSession struct {
	conn    *websocket.Conn
	deps    voiceDeps
	writeMu sync.Mutex
}

// run drives the conversation until the socket closes or the turn fails.
func (s *voiceSession) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conv, err := s.deps.openConversation()
	if err != nil {
		s.fail("open conversation", err)
		return
	}
	defer func() { _ = conv.Close() }()

	responses, err := conv.Response()
	if err != nil {
		s.fail("open response stream", err)
		return
	}

	go s.pumpResponses(responses)
	s.send(outboundEvent{Type: eventReady})
	s.pumpInbound(ctx, conv)
}

// pumpInbound relays client frames into the conversation. Binary frames are
// audio; text frames are control.
func (s *voiceSession) pumpInbound(ctx context.Context, conv duplexConversation) {
	for {
		msgType, data, err := s.conn.ReadMessage()
		if err != nil {
			// A closed socket is the normal end of a call, not a failure.
			return
		}

		switch msgType {
		case websocket.BinaryMessage:
			chunk := &providers.StreamChunk{
				MediaData: &providers.StreamMediaData{
					Data:       data,
					MIMEType:   audioMIMEType,
					SampleRate: audioSampleRate,
					Channels:   audioChannels,
				},
			}
			if err := conv.SendChunk(ctx, chunk); err != nil {
				s.fail("send audio", err)
				return
			}

		case websocket.TextMessage:
			s.handleControl(ctx, conv, data)
		}
	}
}

// handleControl acts on a text control frame. An unparseable or unknown frame
// is ignored rather than closing the call: a client sending something this
// runtime does not understand should not lose its audio.
func (s *voiceSession) handleControl(ctx context.Context, conv duplexConversation, data []byte) {
	var frame controlFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		s.deps.Log.Warn("ignoring unparseable control frame", "error", err)
		return
	}

	if frame.Type == controlInterrupt {
		// Barge-in: end the turn in flight. The pipeline also interrupts on
		// VAD-detected speech; this is the explicit signal.
		if err := conv.SendText(ctx, ""); err != nil {
			s.deps.Log.Warn("interrupt failed", "error", err)
		}
	}
}

// pumpResponses relays the conversation's output back to the client: synthesized
// audio as binary frames, text and transcripts as events.
func (s *voiceSession) pumpResponses(responses <-chan providers.StreamChunk) {
	for chunk := range responses {
		if chunk.MediaData != nil && len(chunk.MediaData.Data) > 0 {
			s.sendBinary(chunk.MediaData.Data)
		}
		if chunk.Delta != "" {
			s.send(outboundEvent{Type: eventText, Text: chunk.Delta})
		}
		if transcript, ok := chunk.Metadata["input_transcription"].(string); ok && transcript != "" {
			s.send(outboundEvent{Type: eventTranscript, Text: transcript})
		}
	}
	s.send(outboundEvent{Type: eventDone})
}

// send writes a text event, ignoring a write to an already-closed socket.
func (s *voiceSession) send(event outboundEvent) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return
	}
	s.write(websocket.TextMessage, encoded)
}

// sendBinary writes one audio frame.
func (s *voiceSession) sendBinary(data []byte) {
	s.write(websocket.BinaryMessage, data)
}

// write serializes access to the connection's single writer.
func (s *voiceSession) write(messageType int, data []byte) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_ = s.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	_ = s.conn.WriteMessage(messageType, data)
}

// fail reports an error to the client and closes with the platform's own code
// for an unhandled handler error.
func (s *voiceSession) fail(what string, err error) {
	s.deps.Log.Error("voice session failed", "op", what, "error", err)
	s.send(outboundEvent{Type: eventError, Error: what + ": " + err.Error()})

	// WriteControl is safe alongside WriteMessage, but take the lock anyway so
	// the close frame cannot interleave with a half-written event.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(wsCloseInternalError, what),
		time.Now().Add(wsWriteTimeout),
	)
}
