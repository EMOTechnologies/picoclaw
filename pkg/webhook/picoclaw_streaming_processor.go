package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	completionMarker       = "==!== process_end ==!=="
	waitingUserInputMarker = "==!== process_waiting_user_input ==!=="
)

// StreamingCallback is called for each message chunk received
type StreamingCallback func(chunk string, isComplete bool) error

// PicoClawStreamingProcessor creates a processor that streams AI responses via callbacks
func PicoClawStreamingProcessor(wsURL, token string, webhookURL string, jobID string) ProcessorFunc {
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

		// Extract session_id from context if available
		sessionID := ""
		if val := ctx.Value("session_id"); val != nil {
			sessionID, _ = val.(string)
		}

		// Call PicoClaw AI via WebSocket with streaming callbacks
		fullResponse, messageCount, err := streamPicoClawAI(ctx, wsURL, token, promptStr, sessionID, webhookURL, jobID)
		if err != nil {
			return nil, fmt.Errorf("AI processing failed: %w", err)
		}

		// Return final result
		return map[string]interface{}{
			"data":          fullResponse,
			"message_count": messageCount,
			"error":         nil,
		}, nil
	}
}

// streamPicoClawAI connects to PicoClaw and streams responses via webhook callbacks
func streamPicoClawAI(ctx context.Context, wsURL, token, prompt, sessionID, webhookURL, jobID string) (string, int, error) {
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
		return "", 0, fmt.Errorf("failed to connect to PicoClaw: %w", err)
	}
	defer conn.Close()

		// Send message using Pico Protocol format
		// Add instruction to output lifecycle markers:
		// - waiting marker when blocked on user input (OTP/CAPTCHA/etc.)
		// - completion marker only when workflow is truly done
		promptWithMarker := prompt + "\n\nIMPORTANT:\n- If you are waiting for user input or user action (OTP/MFA/CAPTCHA/password/API keys/payment info/any other action required by the user to continue), output exactly this marker on a new line at the end of your response: ==!== process_waiting_user_input ==!==\n- Only when the workflow is fully complete, output exactly this marker on a new line at the end of your response: ==!== process_end ==!=="

	message := map[string]interface{}{
		"type":      "message.send",
		"timestamp": time.Now().UnixMilli(),
		"payload": map[string]interface{}{
			"content": promptWithMarker,
		},
	}

	if err := conn.WriteJSON(message); err != nil {
		return "", 0, fmt.Errorf("failed to send message: %w", err)
	}

	logger.InfoC("webhook", fmt.Sprintf("Sent prompt to PicoClaw for job %s", jobID))

	// Read responses and send webhook callback for each chunk
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
				logger.InfoC("webhook", fmt.Sprintf("Max wait time reached for job %s, collected %d messages", jobID, messageCount))
				// Send final completion callback
				sendStreamingWebhook(webhookURL, jobID, sessionID, "", true, fullResponse, messageCount, nil, "")
				// Close connection immediately after completion
				conn.Close()
				return fullResponse, messageCount, nil
			}
			return "", 0, fmt.Errorf("no response received within maximum wait time")
		}

		// Set a read deadline to detect when stream is complete
		conn.SetReadDeadline(time.Now().Add(idleTimeout))

		select {
		case <-ctx.Done():
			if fullResponse != "" {
				logger.InfoC("webhook", fmt.Sprintf("Context cancelled for job %s, returning partial response", jobID))
				sendStreamingWebhook(webhookURL, jobID, sessionID, "", true, fullResponse, messageCount, nil, "")
				// Close connection immediately
				conn.Close()
				return fullResponse, messageCount, nil
			}
			return "", 0, ctx.Err()
		default:
		}

		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)

		if err != nil {
			// Check if this is a timeout (means stream is complete)
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				if receivedFirstMessage && fullResponse != "" {
					logger.InfoC("webhook", fmt.Sprintf("Stream complete for job %s: %d messages, %d chars", jobID, messageCount, len(fullResponse)))
					// Send final completion callback
					sendStreamingWebhook(webhookURL, jobID, sessionID, "", true, fullResponse, messageCount, nil, "")
					// Close connection immediately after completion
					conn.Close()
					return fullResponse, messageCount, nil
				}
				// No response yet, keep waiting
				logger.DebugC("webhook", fmt.Sprintf("Timeout waiting for messages (job %s), continuing...", jobID))
				continue
			}

			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				if fullResponse != "" {
					logger.InfoC("webhook", fmt.Sprintf("Connection closed for job %s, %d messages collected", jobID, messageCount))
					sendStreamingWebhook(webhookURL, jobID, sessionID, "", true, fullResponse, messageCount, nil, "")
					// Connection already closed by error
					return fullResponse, messageCount, nil
				}
				break
			}
			return "", 0, fmt.Errorf("failed to read response: %w", err)
		}

		msgType, _ := msg["type"].(string)
		logger.DebugC("webhook", fmt.Sprintf("Job %s received message type: %s", jobID, msgType))

		switch msgType {
		case "message.create":
			// Extract content from payload
			if payload, ok := msg["payload"].(map[string]interface{}); ok {
				// Check if this is a thought message (skip it)
				if thought, ok := payload["thought"].(bool); ok && thought {
					logger.DebugC("webhook", fmt.Sprintf("Received thought message for job %s (skipping)", jobID))
					continue
				}

				if content, ok := payload["content"].(string); ok && content != "" {
					messageCount++
					receivedFirstMessage = true
					fullResponse += content

					logger.InfoC("webhook", fmt.Sprintf("Job %s - Chunk %d: +%d chars (total: %d)", jobID, messageCount, len(content), len(fullResponse)))

					// Check if this chunk contains waiting-for-user-input marker
					if contains := checkWaitingUserInputMarker(content); contains {
						logger.InfoC("webhook", fmt.Sprintf("Detected waiting-for-user-input marker in job %s", jobID))

						// Remove the marker from the response/chunk
						fullResponse = removeWaitingUserInputMarker(fullResponse)
						cleanContent := removeWaitingUserInputMarker(content)

						// Send streaming chunk first if there's remaining text
						if cleanContent != "" {
							if err := sendStreamingWebhook(webhookURL, jobID, sessionID, cleanContent, false, fullResponse, messageCount, nil, ""); err != nil {
								logger.ErrorC("webhook", fmt.Sprintf("Failed to send webhook callback for job %s chunk %d: %v", jobID, messageCount, err))
							}
						}

					// Send explicit waiting status without completing job
					sendStreamingWebhook(webhookURL, jobID, sessionID, cleanContent, false, fullResponse, messageCount, nil, "waiting_user_input")
					conn.Close()
					return fullResponse, messageCount, nil
				}

					// Check if this chunk contains the completion marker
					if contains := checkCompletionMarker(content); contains {
						logger.InfoC("webhook", fmt.Sprintf("Detected completion marker in job %s", jobID))

						// Remove the marker from the response
						fullResponse = removeCompletionMarker(fullResponse)

						// Send final chunk without the marker
						cleanContent := removeCompletionMarker(content)
						if cleanContent != "" {
							if err := sendStreamingWebhook(webhookURL, jobID, sessionID, cleanContent, false, fullResponse, messageCount, nil, ""); err != nil {
								logger.ErrorC("webhook", fmt.Sprintf("Failed to send webhook callback for job %s chunk %d: %v", jobID, messageCount, err))
							}
						}

						// Send completion callback
						sendStreamingWebhook(webhookURL, jobID, sessionID, "", true, fullResponse, messageCount, nil, "")
						conn.Close()
						return fullResponse, messageCount, nil
					}

					// Send webhook callback for this chunk
					if err := sendStreamingWebhook(webhookURL, jobID, sessionID, content, false, fullResponse, messageCount, nil, ""); err != nil {
						logger.ErrorC("webhook", fmt.Sprintf("Failed to send webhook callback for job %s chunk %d: %v", jobID, messageCount, err))
						// Continue processing even if webhook fails
					}
				}
			}
		case "typing.start":
			logger.DebugC("webhook", fmt.Sprintf("AI started typing for job %s", jobID))
			continue
		case "typing.stop":
			logger.DebugC("webhook", fmt.Sprintf("AI stopped typing for job %s", jobID))
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
			logger.ErrorC("webhook", fmt.Sprintf("AI returned error for job %s: %s", jobID, errorMsg))
			// Send error webhook
			sendStreamingWebhook(webhookURL, jobID, sessionID, "", true, "", 0, fmt.Errorf("%s", errorMsg), "")
			// Close connection immediately after error
			conn.Close()
			return "", 0, fmt.Errorf("AI error: %s", errorMsg)
		case "pong":
			continue
		default:
			logger.DebugC("webhook", fmt.Sprintf("Received unknown message type for job %s: %s", jobID, msgType))
		}
	}

	if fullResponse == "" {
		return "No response received", 0, nil
	}

	return fullResponse, messageCount, nil
}

// sendStreamingWebhook sends a webhook callback for each chunk
func sendStreamingWebhook(webhookURL, jobID, sessionID, chunk string, isComplete bool, fullResponse string, messageCount int, err error, explicitStatus string) error {
	message := chunk
	if isComplete {
		message = fullResponse
	}
	if message == "" && fullResponse != "" {
		message = fullResponse
	}

	payload := map[string]interface{}{
		"job_id":        jobID,
		"session_id":    sessionID,
		"timestamp":     time.Now(),
		"is_complete":   isComplete,
		"message_count": messageCount,
		"message":       message,
		"accumulated_length": len(fullResponse),
	}

	if err != nil {
		payload["status"] = "failed"
		payload["error"] = err.Error()
	} else if explicitStatus == "waiting_user_input" {
		payload["status"] = "waiting_user_input"
		payload["message"] = ""
	} else if isComplete {
		payload["status"] = "completed"
		payload["message"] = ""
		// Preserve result metadata for compatibility with existing consumers.
		payload["result"] = map[string]interface{}{
			"message_count": messageCount,
			"error":         nil,
		}
	} else {
		payload["status"] = "streaming"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req, err := NewWebhookRequest(webhookURL, body)
	if err != nil {
		return err
	}

	return SendWebhookRequest(req)
}

// checkCompletionMarker checks if the content contains the completion marker
func checkCompletionMarker(content string) bool {
	return strings.Contains(content, completionMarker)
}

// checkWaitingUserInputMarker checks if content contains waiting marker
func checkWaitingUserInputMarker(content string) bool {
	return strings.Contains(content, waitingUserInputMarker)
}

// removeCompletionMarker removes the completion marker from the content
func removeCompletionMarker(content string) string {
	return strings.ReplaceAll(content, completionMarker, "")
}

// removeWaitingUserInputMarker removes the waiting marker from the content
func removeWaitingUserInputMarker(content string) string {
	return strings.ReplaceAll(content, waitingUserInputMarker, "")
}
