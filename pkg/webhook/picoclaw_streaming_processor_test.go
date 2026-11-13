package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type callbackRecorder struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (r *callbackRecorder) handler(w http.ResponseWriter, req *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err == nil {
		r.mu.Lock()
		r.payloads = append(r.payloads, payload)
		r.mu.Unlock()
	}
	w.WriteHeader(http.StatusOK)
}

func (r *callbackRecorder) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.payloads...)
}

func newFakeGateway(t *testing.T, frames []map[string]any) string {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var incoming map[string]any
		if err := conn.ReadJSON(&incoming); err != nil {
			return
		}
		for _, frame := range frames {
			if err := conn.WriteJSON(frame); err != nil {
				return
			}
		}
		_, _, _ = conn.ReadMessage()
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func turnUsageFrame(msgType, content string, input, output int) map[string]any {
	payload := map[string]any{
		"turn_usage": map[string]any{
			"input_tokens":  input,
			"output_tokens": output,
			"total_tokens":  input + output,
			"model":         "openai/gpt-4o",
		},
	}
	if content != "" {
		payload["content"] = content
	}
	return map[string]any{"type": msgType, "payload": payload}
}

func TestStreamPicoClawAI_TerminalCallbacksCarryLatestTurnUsage(t *testing.T) {
	tests := []struct {
		name         string
		finalContent string
		wantStatus   string
	}{
		{name: "completed", finalContent: "scraped data\n" + completionMarker, wantStatus: "completed"},
		{name: "waiting user input", finalContent: "enter OTP\n" + waitingUserInputMarker, wantStatus: "waiting_user_input"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wsURL := newFakeGateway(t, []map[string]any{
				turnUsageFrame("message.create", "working on it", 100, 10),
				turnUsageFrame("message.update", "", 250, 25),
				turnUsageFrame("message.create", tt.finalContent, 400, 40),
			})
			recorder := &callbackRecorder{}
			callback := httptest.NewServer(http.HandlerFunc(recorder.handler))
			defer callback.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, _, err := streamPicoClawAI(ctx, wsURL, "token", "scrape", "sess-1", callback.URL, "job-1"); err != nil {
				t.Fatalf("streamPicoClawAI() error = %v", err)
			}

			payloads := recorder.snapshot()
			var terminal map[string]any
			for _, payload := range payloads {
				if payload["status"] == "streaming" {
					if _, ok := payload["usage"]; ok {
						t.Fatalf("streaming callback carries usage: %#v", payload)
					}
					continue
				}
				terminal = payload
			}
			if terminal == nil {
				t.Fatalf("no terminal callback in %#v", payloads)
			}
			if terminal["status"] != tt.wantStatus {
				t.Fatalf("terminal status = %#v, want %q", terminal["status"], tt.wantStatus)
			}
			usage, ok := terminal["usage"].(map[string]any)
			if !ok {
				t.Fatalf("terminal usage = %#v, want map", terminal["usage"])
			}
			if usage["input_tokens"] != float64(400) || usage["output_tokens"] != float64(40) ||
				usage["total_tokens"] != float64(440) || usage["model"] != "openai/gpt-4o" {
				t.Fatalf("terminal usage = %#v, want latest cumulative usage", usage)
			}
		})
	}
}

func TestStreamPicoClawAI_ErrorCallbackCarriesUsageSeenSoFar(t *testing.T) {
	wsURL := newFakeGateway(t, []map[string]any{
		turnUsageFrame("message.create", "working on it", 100, 10),
		{"type": "error", "payload": map[string]any{"message": "boom"}},
	})
	recorder := &callbackRecorder{}
	callback := httptest.NewServer(http.HandlerFunc(recorder.handler))
	defer callback.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err := streamPicoClawAI(ctx, wsURL, "token", "scrape", "sess-1", callback.URL, "job-1"); err == nil {
		t.Fatal("streamPicoClawAI() error = nil, want AI error")
	}

	payloads := recorder.snapshot()
	last := payloads[len(payloads)-1]
	if last["status"] != "failed" {
		t.Fatalf("last status = %#v, want failed", last["status"])
	}
	usage, ok := last["usage"].(map[string]any)
	if !ok || usage["total_tokens"] != float64(110) {
		t.Fatalf("failed usage = %#v, want total_tokens 110", last["usage"])
	}
}
