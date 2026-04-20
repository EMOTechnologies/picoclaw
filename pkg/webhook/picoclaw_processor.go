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

		// Call PicoClaw AI via WebSocket
		response, err := callPicoClawAI(ctx, wsURL, token, promptStr)
		if err != nil {
			return nil, fmt.Errorf("AI processing failed: %w", err)
		}

		// Return result in expected format
		return map[string]interface{}{
			"data":  response,
			"error": nil,
		}, nil
	}
}

// callPicoClawAI sends a message to PicoClaw via WebSocket and waits for response
func callPicoClawAI(ctx context.Context, wsURL, token, prompt string) (string, error) {
	// Set up WebSocket connection with timeout
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	// Add token to Authorization header (Bearer authentication)
	headers := map[string][]string{
		"Authorization": {"Bearer " + token},
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return "", fmt.Errorf("failed to connect to PicoClaw: %w", err)
	}
	defer conn.Close()

	// Set read deadline
	deadline := time.Now().Add(2 * time.Minute)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	conn.SetReadDeadline(deadline)

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
	idleTimeout := 2 * time.Second  // Wait 2 seconds after last chunk
	maxWaitTime := 5 * time.Minute  // Maximum total wait time
	startTime := time.Now()

	for {
		// Check overall timeout
		if time.Since(startTime) > maxWaitTime {
			if fullResponse != "" {
				logger.InfoC("webhook", fmt.Sprintf("Max wait time reached, returning collected response (%d messages)", messageCount))
				return fullResponse, nil
			}
			return "", fmt.Errorf("no response received within maximum wait time")
		}

		// Set a read deadline to detect when stream is complete
		// This resets with each iteration, so as long as messages keep coming, we continue
		conn.SetReadDeadline(time.Now().Add(idleTimeout))

		select {
		case <-ctx.Done():
			if fullResponse != "" {
				logger.InfoC("webhook", "Context cancelled, returning partial response")
				return fullResponse, nil
			}
			return "", ctx.Err()
		default:
		}

		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)

		if err != nil {
			// Check if this is a timeout (means stream is complete)
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				if fullResponse != "" {
					logger.InfoC("webhook", fmt.Sprintf("Stream complete: received %d message chunks, total length: %d", messageCount, len(fullResponse)))
					return fullResponse, nil
				}
				// No response yet, keep waiting
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
