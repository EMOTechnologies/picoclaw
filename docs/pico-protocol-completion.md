# Pico Protocol Completion Signal

## Overview

The Pico Protocol now includes an explicit completion marker that indicates when the AI has finished responding to a message. This eliminates the need for long timeout-based completion detection.

## Lifecycle Markers

### `==!== process_end ==!==`

When the AI finishes processing, PicoClaw sends a special marker message as the last `message.create`:

**Message Format:**
```json
{
  "type": "message.create",
  "session_id": "session-uuid",
  "timestamp": 1234567890,
  "payload": {
    "content": "==!== process_end ==!==",
    "thought": false
  }
}
```

This marker is:
- ✅ Sent as a regular message (compatible with existing protocol)
- ✅ Always the last message in a response
- ✅ Easy to detect by clients
- ✅ Backward compatible (old clients just ignore it)

### `==!== process_waiting_user_input ==!==`

When the AI is blocked and needs user input (OTP/MFA/CAPTCHA/password/API key/payment info), it should send this marker at the end of the current response.

This marker means:
- ✅ Workflow is paused, not finished
- ✅ Client should emit `status: "waiting_user_input"` with `is_complete: false`
- ✅ Processing should resume after user input is provided

## Message Flow

```
Client                          Server (Gateway)
  |                                    |
  |  message.send                      |
  |  {"prompt": "Hello"}               |
  | ---------------------------------> |
  |                                    |
  |            typing.start            |
  | <--------------------------------- |
  |                                    |
  |   message.create (chunk 1)         |
  |   "Hello"                          |
  | <--------------------------------- |
  |                                    |
  |   message.create (chunk 2)         |
  |   "there!"                         |
  | <--------------------------------- |
  |                                    |
  |            typing.stop             |
  | <--------------------------------- |
  |                                    |
  |   message.create (marker) ⭐       |
  |   "==!== process_end ==!=="        |
  | <--------------------------------- |
  |                                    |
```

## When is the Marker Sent?

The completion marker is sent when:

1. ✅ **AI finishes generating response** - After the last content chunk
2. ✅ **Typing indicator stops** - Sent immediately after `typing.stop`
3. ✅ **Before connection would idle** - No need to wait for timeout

## Benefits

### Before (Timeout-Based)
- ❌ Had to wait 1 minute of idle time to detect completion
- ❌ Slow response time for short messages
- ❌ Risk of premature timeout for long-thinking AI
- ❌ Wasted resources keeping connection open

### After (Signal-Based)
- ✅ **Instant completion detection** - No waiting for timeout
- ✅ **Fast for short messages** - Completes in ~2 seconds instead of 60+
- ✅ **Reliable for long messages** - No risk of timeout
- ✅ **Efficient resource usage** - Connection closes immediately

## Webhook Processing Impact

### Completion Detection Order

The webhook processor now detects lifecycle events via:

1. **Waiting marker `==!== process_waiting_user_input ==!==`** → `waiting_user_input` (non-terminal)
2. **Completion marker `==!== process_end ==!==`** → `completed` (terminal)
3. Idle timeout (3 minutes - fallback terminal)
4. WebSocket close (fallback terminal)
5. Error message (`failed`)
6. Context timeout (5 minutes)

### Timing Improvement

**Before:**
```
Message received → Wait 60 seconds → Send final callback
Total: 60+ seconds after last message
```

**After:**
```
Message received → message.complete → Send final callback
Total: ~2 seconds after last message
```

## Implementation Details

### Server-Side (Gateway)

Modified `/pkg/channels/pico/pico.go`:
```go
func (c *PicoChannel) StartTyping(...) (func(), error) {
    startMsg := newMessage(TypeTypingStart, nil)
    c.broadcastToSession(chatID, startMsg)
    
    return func() {
        stopMsg := newMessage(TypeTypingStop, nil)
        c.broadcastToSession(chatID, stopMsg)
        
        // Send completion marker as a regular message
        markerMsg := newMessage(TypeMessageCreate, map[string]any{
            PayloadKeyContent: "==!== process_end ==!==",
            PayloadKeyThought: false,
        })
        c.broadcastToSession(chatID, markerMsg)
    }, nil
}
```

### Client-Side (Webhook Processor)

Detects the completion marker:
```go
case "message.create":
    if payload, ok := msg["payload"].(map[string]interface{}); ok {
        if content, ok := payload["content"].(string); ok && content != "" {
            // Check for completion marker
            if content == "==!== process_end ==!==" {
                logger.InfoC("webhook", "Received completion marker")
                sendStreamingWebhook(..., true, ...)  // Final callback
                conn.Close()
                return fullResponse, messageCount, nil
            }
            
            // Regular content - send as streaming chunk
            fullResponse += content
            sendStreamingWebhook(..., false, ...)
        }
    }
```

## Backward Compatibility

✅ **Fully backward compatible**

- Old clients ignore the `message.complete` message (unknown type)
- Old clients continue using timeout-based detection
- New clients get instant completion via the signal
- Timeout fallback still works if signal is missed

## Testing

### Test Completion Signal

```bash
# 1. Send a prompt
curl -X POST http://localhost:18800/api/webhook/process \
  -H "Content-Type: application/json" \
  -d '{
    "webhook_url": "https://webhook.site/your-id",
    "session_id": "test-session",
    "payload": {"prompt": "Say hello"}
  }'

# 2. Watch webhook.site for callbacks
# You should see:
# - Multiple "streaming" callbacks (chunks)
# - Final "completed" callback within 2-3 seconds after last chunk
```

### Expected Timeline

```
0.0s: Request sent
0.5s: First chunk received → webhook callback
1.0s: Second chunk received → webhook callback  
1.5s: Third chunk received → webhook callback
2.0s: message.complete received → final webhook callback ✅
```

Compare to old behavior:
```
0.0s: Request sent
0.5s: First chunk received → webhook callback
1.0s: Second chunk received → webhook callback
1.5s: Third chunk received → webhook callback
61.5s: Timeout detected → final webhook callback ❌
```

## Monitoring

### Log Messages

**When completion is detected via marker:**
```
[webhook] Received completion marker for job abc123: 5 messages, 150 chars
```

**When completion falls back to timeout:**
```
[webhook] Stream complete for job abc123: 5 messages, 150 chars (timeout)
```

### Metrics

Track completion method:
- `completion_via_signal` - Fast path (desired)
- `completion_via_timeout` - Slow path (fallback)

## Configuration

### Timeout Settings

Now that we have the completion marker, timeouts are just fallbacks:

```go
idleTimeout := 3 * time.Minute   // Fallback if marker missed
maxWaitTime := 5 * time.Minute   // Safety maximum
```

## Future Enhancements

Possible additions:
- `message.complete` with metadata (token count, finish_reason)
- Progress updates (`message.progress`)
- Cancellation support (`message.cancel`)

## Related Documentation

- [Pico Protocol Overview](../pkg/channels/README.md)
- [Webhook Processing](webhook-ai-integration.md)
- [Session Management](webhook-processing.md)
