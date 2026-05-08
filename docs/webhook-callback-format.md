# Webhook Callback Format

## Overview

When you submit a webhook processing request, your webhook endpoint will receive multiple callbacks as the AI streams its response.

## Callback Types

### 1. Streaming Callback (Per Message Chunk)

Sent for each message chunk as the AI generates the response.

```json
{
  "job_id": "f6d476c7-4d2f-4956-96b2-9137138d32e7",
  "session_id": "1e052a24-bd5e-40a9-b418-8824e6d511ba",
  "status": "streaming",
  "message": "This is part of the AI response",
  "accumulated_length": 150,
  "message_count": 3,
  "is_complete": false,
  "timestamp": "2026-04-22T05:00:01.123Z"
}
```

**Fields:**
- `job_id` - Unique identifier for this processing job
- `session_id` - Session ID for conversation context
- `status` - Always `"streaming"` for intermediate chunks
- `message` - The actual message content chunk
- `accumulated_length` - Total length of all messages received so far
- `message_count` - Number of message chunks received so far
- `is_complete` - Always `false` for streaming chunks
- `timestamp` - When this callback was sent

### 2. Completion Callback (Final)

Sent after 3 minutes of idle time or when processing completes.

```json
{
  "job_id": "f6d476c7-4d2f-4956-96b2-9137138d32e7",
  "session_id": "1e052a24-bd5e-40a9-b418-8824e6d511ba",
  "status": "completed",
  "result": {
    "message_count": 10,
    "error": null
  },
  "is_complete": true,
  "message_count": 10,
  "timestamp": "2026-04-22T05:03:01.456Z"
}
```

**Fields:**
- `job_id` - Same job ID from streaming callbacks
- `session_id` - Same session ID
- `status` - `"completed"` when successful
- `result` - Summary information
  - `message_count` - Total number of message chunks sent
  - `error` - Always `null` on success
- `is_complete` - Always `true` for final callback
- `message_count` - Total chunks sent (same as `result.message_count`)
- `timestamp` - When completion was detected

**Note:** The full response text is NOT included in the completion callback. Your webhook should reconstruct it from the streaming `message` chunks.

### 3. Error Callback

Sent if processing fails.

```json
{
  "job_id": "f6d476c7-4d2f-4956-96b2-9137138d32e7",
  "session_id": "1e052a24-bd5e-40a9-b418-8824e6d511ba",
  "status": "failed",
  "error": "AI processing failed: connection timeout",
  "is_complete": true,
  "message_count": 0,
  "timestamp": "2026-04-22T05:00:30.789Z"
}
```

**Fields:**
- `status` - `"failed"` when error occurs
- `error` - Description of what went wrong
- `is_complete` - Always `true` for error callbacks
- `message_count` - Number of chunks sent before error

## Full Example Flow

### Request
```bash
POST /api/webhook/process
{
  "webhook_url": "https://your-app.com/webhook",
  "session_id": "user-123",
  "payload": {
    "prompt": "What is Python?"
  }
}
```

### Response (Immediate)
```json
{
  "job_id": "abc-123",
  "session_id": "user-123",
  "status": "processing",
  "timestamp": "2026-04-22T05:00:00Z"
}
```

### Webhook Callbacks (Streamed)

**Callback 1 (streaming):**
```json
{
  "job_id": "abc-123",
  "session_id": "user-123",
  "status": "streaming",
  "message": "Python is a high-level,",
  "accumulated_length": 25,
  "message_count": 1,
  "is_complete": false,
  "timestamp": "2026-04-22T05:00:02Z"
}
```

**Callback 2 (streaming):**
```json
{
  "job_id": "abc-123",
  "session_id": "user-123",
  "status": "streaming",
  "message": " interpreted programming language",
  "accumulated_length": 57,
  "message_count": 2,
  "is_complete": false,
  "timestamp": "2026-04-22T05:00:03Z"
}
```

**Callback 3 (streaming):**
```json
{
  "job_id": "abc-123",
  "session_id": "user-123",
  "status": "streaming",
  "message": " known for its simplicity.",
  "accumulated_length": 83,
  "message_count": 3,
  "is_complete": false,
  "timestamp": "2026-04-22T05:00:04Z"
}
```

**Callback 4 (completion):**
```json
{
  "job_id": "abc-123",
  "session_id": "user-123",
  "status": "completed",
  "result": {
    "message_count": 3,
    "error": null
  },
  "is_complete": true,
  "message_count": 3,
  "timestamp": "2026-04-22T05:03:04Z"
}
```

## Reconstructing Full Response

Your webhook endpoint should collect all streaming messages:

```python
# Example webhook handler
responses = {}  # job_id -> accumulated response

@app.post('/webhook')
def handle_webhook(data):
    job_id = data['job_id']
    
    if data['status'] == 'streaming':
        # Accumulate message chunks
        if job_id not in responses:
            responses[job_id] = ""
        responses[job_id] += data['message']
        
        print(f"Received chunk: {data['message']}")
        print(f"Total so far: {responses[job_id]}")
        
    elif data['status'] == 'completed':
        # Processing complete
        full_response = responses.get(job_id, "")
        print(f"Complete response: {full_response}")
        
        # Process final response
        process_final_response(full_response)
        
        # Cleanup
        del responses[job_id]
        
    elif data['status'] == 'failed':
        # Handle error
        print(f"Error: {data['error']}")
        
    return {'status': 'ok'}
```

## Key Points

✅ **Multiple Callbacks** - Expect multiple webhooks per job
✅ **Message Field** - Streaming chunks use `message` key (not `chunk`)
✅ **Accumulation** - Collect all `message` values to get full response
✅ **Completion Detection** - Look for `is_complete: true` and `status: "completed"`
✅ **Session ID** - Use same session_id for conversation context
✅ **Order Guaranteed** - Messages arrive in order sent

## Timing

- **First chunk**: Usually within 2-5 seconds of request
- **Subsequent chunks**: As fast as AI generates (~0.5-2 seconds apart)
- **Completion**: 3 minutes after last chunk (idle timeout)

## Related

- [Webhook Processing Guide](webhook-processing.md)
- [Session Management](webhook-session-management.md)
- [Integration Examples](../examples/webhook-processing/)
