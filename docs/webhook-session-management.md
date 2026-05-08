# Webhook Session Management

## Overview

The webhook processing system now includes proper session management to prevent duplicate processing and stale connections when multiple requests use the same `session_id`.

## Problem

**Before:**
```
Request 1: session_id=ABC → WebSocket 1 (active)
Request 2: session_id=ABC → WebSocket 2 (active)

Result: Both connections listening → duplicate messages
```

## Solution

**After:**
```
Request 1: session_id=ABC → WebSocket 1 (active)
Request 2: session_id=ABC → Cancel WebSocket 1 → WebSocket 2 (active)

Result: Only one connection → no duplicates
```

## How It Works

### 1. Connection Tracking

The webhook processor tracks active WebSocket connections by `session_id`:

```go
type Processor struct {
    activeConns map[string]context.CancelFunc // session_id -> cancel function
}
```

### 2. Cancellation Flow

When a new request arrives with an existing `session_id`:

```
1. Check if session_id already has an active connection
2. If yes → cancel the old connection
3. Register new connection with session_id
4. Process the new request
5. Unregister when complete
```

### 3. Automatic Cleanup

Connections are automatically unregistered when:
- Job completes successfully
- Job encounters an error
- Context is cancelled
- Connection times out

## Example Scenario

### Scenario 1: User Interrupts Previous Request

```bash
# User sends first request
POST /api/webhook/process
{
  "session_id": "user-123",
  "payload": {"prompt": "Write a long essay..."}
}
# → WebSocket connection established

# User quickly sends second request (different question)
POST /api/webhook/process  
{
  "session_id": "user-123",
  "payload": {"prompt": "What's 2+2?"}
}
# → Previous connection cancelled
# → New connection established
# → Only second request processes
```

**Result:** User gets answer to "What's 2+2?" immediately, without waiting for the essay.

### Scenario 2: Conversation Flow

```bash
# First message in conversation
POST /api/webhook/process
{
  "session_id": "conv-456",
  "payload": {"prompt": "What is Python?"}
}
# → Processes and completes

# Second message in same conversation
POST /api/webhook/process
{
  "session_id": "conv-456", 
  "payload": {"prompt": "Show me an example"}
}
# → Uses same session (conversation context)
# → No old connection to cancel (previous completed)
# → Processes normally
```

**Result:** Both messages processed successfully with conversation context maintained.

## Implementation Details

### Connection Registration

```go
func (p *Processor) processJob(job *Job) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    // Cancel any existing connection for this session
    p.cancelExistingConnection(job.SessionID)

    // Register this connection
    p.registerConnection(job.SessionID, cancel)
    defer p.unregisterConnection(job.SessionID)

    // ... process request
}
```

### Cancellation Logic

```go
func (p *Processor) cancelExistingConnection(sessionID string) {
    if cancel, exists := p.activeConns[sessionID]; exists {
        logger.InfoC("webhook", "Cancelling existing connection for session")
        cancel() // Cancels the context, closing WebSocket
        delete(p.activeConns, sessionID)
    }
}
```

## Behavior

### With Same session_id

**Multiple Rapid Requests:**
- Only the most recent request is processed
- Previous requests are cancelled immediately
- No duplicate messages
- No wasted resources

**Sequential Requests:**
- Each request completes before next starts
- No interference
- Conversation context maintained

### With Different session_id

**Concurrent Requests:**
- Each request has its own connection
- All process in parallel
- Independent sessions
- No interference

## Logging

### When Connection is Cancelled

```
[webhook] Cancelling existing connection for session abc-123
[webhook] Context cancelled for job xyz-789, returning partial response
```

### When Connection is Registered

```
[webhook] Registered connection for session abc-123
```

### When Connection Completes

```
[webhook] Unregistered connection for session abc-123
```

## Edge Cases

### 1. Request Arrives During Processing

```
Time  | Session ABC
------|------------------------------------------
0:00  | Request 1 starts processing
0:05  | Request 1 receives first AI chunk
0:10  | Request 2 arrives → cancels Request 1
0:11  | Request 1 context cancelled, stops
0:12  | Request 2 starts fresh
```

### 2. Request Arrives After Completion

```
Time  | Session ABC
------|------------------------------------------
0:00  | Request 1 starts processing
0:30  | Request 1 completes, unregisters
0:35  | Request 2 arrives
0:36  | No existing connection, proceeds normally
```

### 3. Multiple Rapid Requests

```
Time  | Session ABC
------|------------------------------------------
0:00  | Request 1 → Connection 1
0:01  | Request 2 → Cancels 1, Connection 2
0:02  | Request 3 → Cancels 2, Connection 3
0:03  | ... only Connection 3 active
```

**Result:** Only the last request (Request 3) is processed.

## Benefits

✅ **No Duplicate Processing**
- Only one active connection per session at a time
- Previous requests automatically cancelled

✅ **Resource Efficiency**
- Old connections closed immediately
- No zombie connections
- Reduced memory usage

✅ **Better User Experience**
- Latest request takes priority
- No confusion from stale responses
- Fast response time

✅ **Conversation Context**
- Same session_id maintains context
- Each request builds on previous
- Natural conversation flow

## Testing

### Test Duplicate Prevention

```bash
# Terminal 1: Start long request
curl -X POST http://localhost:18800/api/webhook/process \
  -H "Content-Type: application/json" \
  -d '{
    "webhook_url": "https://webhook.site/id-1",
    "session_id": "test-session",
    "payload": {"prompt": "Write a 1000-word essay"}
  }'

# Terminal 2: Immediately send second request (within 1 second)
curl -X POST http://localhost:18800/api/webhook/process \
  -H "Content-Type: application/json" \
  -d '{
    "webhook_url": "https://webhook.site/id-2", 
    "session_id": "test-session",
    "payload": {"prompt": "What is 2+2?"}
  }'
```

**Expected:**
- First webhook receives partial response (cancelled)
- Second webhook receives complete answer to "What is 2+2?"
- No messages from essay request in second webhook

### Test Conversation Flow

```bash
# Message 1
curl -X POST http://localhost:18800/api/webhook/process \
  -d '{
    "webhook_url": "https://webhook.site/my-id",
    "session_id": "conversation-1",
    "payload": {"prompt": "My name is Alice"}
  }'

# Wait for completion, then Message 2
curl -X POST http://localhost:18800/api/webhook/process \
  -d '{
    "webhook_url": "https://webhook.site/my-id",
    "session_id": "conversation-1",
    "payload": {"prompt": "What is my name?"}
  }'
```

**Expected:**
- First message processes completely
- Second message remembers context (answers "Alice")
- No cancellation (first completed before second started)

## Related

- [Webhook Processing Guide](webhook-processing.md)
- [Session Management](webhook-ai-integration.md)
- [Completion Detection](pico-protocol-completion.md)
