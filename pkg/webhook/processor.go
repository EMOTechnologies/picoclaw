package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// ProcessRequest represents an incoming request to process asynchronously
type ProcessRequest struct {
	WebhookURL string                 `json:"webhook_url"`
	Payload    map[string]interface{} `json:"payload"`
	SessionID  string                 `json:"session_id,omitempty"` // Optional session ID for conversation context
}

// ProcessResponse is returned immediately when a job is accepted
type ProcessResponse struct {
	JobID     string    `json:"job_id"`
	SessionID string    `json:"session_id"` // Session ID for conversation context
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
}

// WebhookPayload is sent to the webhook URL when processing completes
type WebhookPayload struct {
	JobID     string                 `json:"job_id"`
	SessionID string                 `json:"session_id"` // Session ID for conversation context
	Status    string                 `json:"status"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// Processor handles async processing and webhook callbacks
type Processor struct {
	mu              sync.RWMutex
	jobs            map[string]*Job
	httpClient      *http.Client
	processorFn     ProcessorFunc
	activeConnsMu   sync.Mutex
	activeConns     map[string]context.CancelFunc // session_id -> cancel function
}

// Job tracks the state of an async job
type Job struct {
	ID          string
	WebhookURL  string
	Payload     map[string]interface{}
	SessionID   string     // Session ID for conversation context
	Status      string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// ProcessorFunc is the actual processing function to be executed
type ProcessorFunc func(ctx context.Context, payload map[string]interface{}) (map[string]interface{}, error)

// NewProcessor creates a new webhook processor
func NewProcessor(processorFn ProcessorFunc) *Processor {
	return &Processor{
		jobs: make(map[string]*Job),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		processorFn: processorFn,
		activeConns: make(map[string]context.CancelFunc),
	}
}

// Submit accepts a new job and returns immediately
func (p *Processor) Submit(req ProcessRequest) (*ProcessResponse, error) {
	if req.WebhookURL == "" {
		return nil, fmt.Errorf("webhook_url is required")
	}

	jobID := uuid.New().String()

	// Use provided session_id or generate a new one
	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	job := &Job{
		ID:         jobID,
		WebhookURL: req.WebhookURL,
		Payload:    req.Payload,
		SessionID:  sessionID,
		Status:     "processing",
		CreatedAt:  time.Now(),
	}

	p.mu.Lock()
	p.jobs[jobID] = job
	p.mu.Unlock()

	// Start processing in background
	go p.processJob(job)

	logger.InfoCF("webhook", "Job submitted", map[string]any{
		"job_id":      jobID,
		"session_id":  sessionID,
		"webhook_url": req.WebhookURL,
	})

	return &ProcessResponse{
		JobID:     jobID,
		SessionID: sessionID,
		Status:    "processing",
		Timestamp: time.Now(),
	}, nil
}

// GetJob retrieves job status
func (p *Processor) GetJob(jobID string) (*Job, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	job, exists := p.jobs[jobID]
	return job, exists
}

// cancelExistingConnection cancels any existing WebSocket connection for the session
func (p *Processor) cancelExistingConnection(sessionID string) {
	if sessionID == "" {
		return
	}

	p.activeConnsMu.Lock()
	defer p.activeConnsMu.Unlock()

	if cancel, exists := p.activeConns[sessionID]; exists {
		logger.InfoC("webhook", fmt.Sprintf("Cancelling existing connection for session %s", sessionID))
		cancel()
		delete(p.activeConns, sessionID)
	}
}

// registerConnection registers a new active connection for the session
func (p *Processor) registerConnection(sessionID string, cancel context.CancelFunc) {
	if sessionID == "" {
		return
	}

	p.activeConnsMu.Lock()
	defer p.activeConnsMu.Unlock()

	p.activeConns[sessionID] = cancel
	logger.DebugC("webhook", fmt.Sprintf("Registered connection for session %s", sessionID))
}

// unregisterConnection removes the connection registration for the session
func (p *Processor) unregisterConnection(sessionID string) {
	if sessionID == "" {
		return
	}

	p.activeConnsMu.Lock()
	defer p.activeConnsMu.Unlock()

	delete(p.activeConns, sessionID)
	logger.DebugC("webhook", fmt.Sprintf("Unregistered connection for session %s", sessionID))
}

// processJob executes the processing and calls webhook
func (p *Processor) processJob(job *Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Cancel any existing connection for this session
	p.cancelExistingConnection(job.SessionID)

	// Register this connection
	p.registerConnection(job.SessionID, cancel)
	defer p.unregisterConnection(job.SessionID)

	// Add webhook info and session to context for streaming processors
	ctx = context.WithValue(ctx, "webhook_url", job.WebhookURL)
	ctx = context.WithValue(ctx, "job_id", job.ID)
	ctx = context.WithValue(ctx, "session_id", job.SessionID)

	logger.InfoCF("webhook", "Processing job started", map[string]any{
		"job_id": job.ID,
	})

	result, err := p.processorFn(ctx, job.Payload)

	completedAt := time.Now()
	job.CompletedAt = &completedAt

	var webhookPayload WebhookPayload
	if err != nil {
		job.Status = "failed"
		webhookPayload = WebhookPayload{
			JobID:     job.ID,
			SessionID: job.SessionID,
			Status:    "failed",
			Error:     err.Error(),
			Timestamp: completedAt,
		}
		logger.ErrorCF("webhook", "Job processing failed", map[string]any{
			"job_id":     job.ID,
			"session_id": job.SessionID,
			"error":      err.Error(),
		})
	} else {
		job.Status = "completed"
		webhookPayload = WebhookPayload{
			JobID:     job.ID,
			SessionID: job.SessionID,
			Status:    "completed",
			Result:    result,
			Timestamp: completedAt,
		}
		logger.InfoCF("webhook", "Job processing completed", map[string]any{
			"job_id":     job.ID,
			"session_id": job.SessionID,
		})
	}

	// Check if streaming mode was used (streaming sends its own final callback)
	isStreamingMode := false
	if result != nil {
		if _, hasMessageCount := result["message_count"]; hasMessageCount {
			isStreamingMode = true
		}
	}

	// Call webhook only if not in streaming mode (streaming already sent final callback)
	if !isStreamingMode {
		if err := p.callWebhook(job.WebhookURL, webhookPayload); err != nil {
			logger.ErrorCF("webhook", "Webhook callback failed", map[string]any{
				"job_id":      job.ID,
				"webhook_url": job.WebhookURL,
				"error":       err.Error(),
			})
		}
	} else {
		logger.DebugC("webhook", fmt.Sprintf("Skipping final webhook for job %s (streaming mode already sent completion)", job.ID))
	}
}

// NewWebhookRequest creates an HTTP request for webhook callback
func NewWebhookRequest(webhookURL string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PicoClaw-Webhook/1.0")

	return req, nil
}

// SendWebhookRequest sends a webhook HTTP request
func SendWebhookRequest(req *http.Request) error {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned non-2xx status: %d", resp.StatusCode)
	}

	return nil
}

// callWebhook sends the result to the webhook URL
func (p *Processor) callWebhook(webhookURL string, payload WebhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	logger.InfoCF("webhook", "Calling webhook", map[string]any{
		"url":    webhookURL,
		"job_id": payload.JobID,
	})

	req, err := NewWebhookRequest(webhookURL, body)
	if err != nil {
		return err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned non-2xx status: %d", resp.StatusCode)
	}

	logger.InfoCF("webhook", "Webhook called successfully", map[string]any{
		"url":         webhookURL,
		"job_id":      payload.JobID,
		"status_code": resp.StatusCode,
	})

	return nil
}

// CleanupOldJobs removes jobs older than the specified duration
func (p *Processor) CleanupOldJobs(maxAge time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for id, job := range p.jobs {
		if job.CreatedAt.Before(cutoff) {
			delete(p.jobs, id)
		}
	}
}
