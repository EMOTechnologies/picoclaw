package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// PicoClawProcessor creates a processor that uses PicoClaw's AI agent
func PicoClawProcessor(wsURL, token string) ProcessorFunc {
	return func(ctx context.Context, payload map[string]interface{}) (map[string]interface{}, error) {
		// Extract prompt from payload
		prompt, ok := payload["prompt"]
		if !ok {
			// If no prompt field, use the entire payload as a string
			promptBytes, err := json.Marshal(payload)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal payload: %w", err)
			}
			prompt = string(promptBytes)
		}

		promptStr, ok := prompt.(string)
		if !ok {
			return nil, fmt.Errorf("prompt must be a string")
		}

		// Extract webhook callback info and session from context if available
		var webhookURL string
		var jobID string
		var sessionID string
		if val := ctx.Value("webhook_url"); val != nil {
			webhookURL, _ = val.(string)
		}
		if val := ctx.Value("job_id"); val != nil {
			jobID, _ = val.(string)
		}
		if val := ctx.Value("session_id"); val != nil {
			sessionID, _ = val.(string)
		}

		// Call PicoClaw AI via WebSocket
		var response string
		var messageCount int
		var err error
		if webhookURL != "" && jobID != "" {
			// Use streaming mode with callbacks
			response, messageCount, err = streamPicoClawAI(ctx, wsURL, token, promptStr, sessionID, webhookURL, jobID)
		} else {
			// Use non-streaming mode
			response, err = callPicoClawAI(ctx, wsURL, token, promptStr, sessionID)
		}

		if err != nil {
			return nil, fmt.Errorf("AI processing failed: %w", err)
		}

		// Return result in expected format
		result := map[string]interface{}{
			"data":  response,
			"error": nil,
		}

		// Add message_count if streaming was used (signals to skip duplicate final webhook)
		if messageCount > 0 {
			result["message_count"] = messageCount
		}

		return result, nil
	}
}

// callPicoClawAI sends a message to PicoClaw via WebSocket and waits for response
func callPicoClawAI(ctx context.Context, wsURL, token, prompt, sessionID string) (string, error) {
	// Set up WebSocket connection with timeout
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// Add token to Authorization header (Bearer authentication)
	headers := map[string][]string{
		"Authorization": {"Bearer " + token},
	}

	// Add session_id to WebSocket URL if provided
	wsURLWithSession := wsURL
	if sessionID != "" {
		wsURLWithSession = fmt.Sprintf("%s?session_id=%s", wsURL, sessionID)
	}

	conn, _, err := dialer.DialContext(ctx, wsURLWithSession, headers)
	if err != nil {
		return "", fmt.Errorf("failed to connect to PicoClaw: %w", err)
	}
	defer conn.Close()

	// Send message using Pico Protocol format
	message := map[string]interface{}{
		"type":      "message.send",
		"timestamp": time.Now().UnixMilli(),
		"payload": map[string]interface{}{
			"content": prompt,
		},
	}

	if err := conn.WriteJSON(message); err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}

	logger.DebugC("webhook", fmt.Sprintf("Sent prompt to PicoClaw: %s", prompt))

	// Read responses until we get a complete answer
	// The Pico protocol streams responses as multiple message.create messages
	var fullResponse string
	var messageCount int
	idleTimeout := 3 * time.Minute  // Fallback timeout (we have completion marker now)
	maxWaitTime := 5 * time.Minute  // Maximum total wait time
	startTime := time.Now()
	receivedFirstMessage := false

	for {
		// Check overall timeout
		if time.Since(startTime) > maxWaitTime {
			if fullResponse != "" {
				logger.InfoC("webhook", fmt.Sprintf("Max wait time reached, returning collected response (%d messages)", messageCount))
				conn.Close()
				return fullResponse, nil
			}
			conn.Close()
			return "", fmt.Errorf("no response received within maximum wait time")
		}

		// Set a read deadline to detect when stream is complete
		// This resets with each iteration, so as long as messages keep coming, we continue
		conn.SetReadDeadline(time.Now().Add(idleTimeout))

		select {
		case <-ctx.Done():
			if fullResponse != "" {
				logger.InfoC("webhook", "Context cancelled, returning partial response")
				conn.Close()
				return fullResponse, nil
			}
			conn.Close()
			return "", ctx.Err()
		default:
		}

		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)

		if err != nil {
			// Check if this is a timeout (means stream is complete)
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				if receivedFirstMessage && fullResponse != "" {
					logger.InfoC("webhook", fmt.Sprintf("Stream complete: received %d message chunks, total length: %d", messageCount, len(fullResponse)))
					conn.Close()
					return fullResponse, nil
				}
				// No response yet, keep waiting
				logger.DebugC("webhook", "Timeout waiting for messages, continuing...")
				continue
			}

			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				if fullResponse != "" {
					logger.InfoC("webhook", fmt.Sprintf("Connection closed, returning response (%d messages)", messageCount))
					return fullResponse, nil
				}
				break
			}
			return "", fmt.Errorf("failed to read response: %w", err)
		}

		msgType, _ := msg["type"].(string)

		switch msgType {
		case "message.create":
			// Extract content from payload
			if payload, ok := msg["payload"].(map[string]interface{}); ok {
				// Check if this is a thought message (skip it)
				if thought, ok := payload["thought"].(bool); ok && thought {
					logger.DebugC("webhook", "Received thought message (skipping)")
					continue
				}

				if content, ok := payload["content"].(string); ok && content != "" {
					messageCount++
					receivedFirstMessage = true
					fullResponse += content
					logger.DebugC("webhook", fmt.Sprintf("Chunk %d: +%d chars (total: %d)", messageCount, len(content), len(fullResponse)))
				}
			}
		case "typing.start":
			logger.DebugC("webhook", "AI started typing")
			continue
		case "typing.stop":
			logger.DebugC("webhook", "AI stopped typing")
			continue
		case "error":
			// Extract error from payload
			errorMsg := "unknown error"
			if payload, ok := msg["payload"].(map[string]interface{}); ok {
				if message, ok := payload["message"].(string); ok {
					errorMsg = message
				} else if code, ok := payload["code"].(string); ok {
					errorMsg = code
				}
			}
			logger.ErrorC("webhook", fmt.Sprintf("AI returned error: %s", errorMsg))
			conn.Close()
			return "", fmt.Errorf("AI error: %s", errorMsg)
		case "pong":
			continue
		default:
			logger.DebugC("webhook", fmt.Sprintf("Received unknown message type: %s", msgType))
		}
	}

	if fullResponse == "" {
		return "No response received", nil
	}

	return fullResponse, nil
}

// CreatePicoClawProcessor creates a processor that uses PicoClaw's AI
// wsURL should be like "ws://localhost:18790/pico/ws" (gateway's Pico channel endpoint)
// token is the composed token (pico-<pid_token><pico_token>)
func CreatePicoClawProcessor(wsURL, token string) *Processor {
	return NewProcessor(PicoClawProcessor(wsURL, token))
}
