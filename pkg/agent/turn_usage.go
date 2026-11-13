// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/backgroundwork"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const backgroundUsageWaitTimeout = 2 * time.Minute

type turnUsage struct {
	mu      sync.Mutex
	calls   int
	input   int
	output  int
	total   int
	model   string
	pending int
	idle    chan struct{}
}

type turnUsageKeyType struct{}

var turnUsageKey = turnUsageKeyType{}

func withTurnUsage(ctx context.Context) context.Context {
	if turnUsageFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, turnUsageKey, &turnUsage{})
}

func contextWithTurnUsage(ctx context.Context, u *turnUsage) context.Context {
	if u == nil {
		return ctx
	}
	return context.WithValue(ctx, turnUsageKey, u)
}

func turnUsageFromContext(ctx context.Context) *turnUsage {
	if ctx == nil {
		return nil
	}
	u, _ := ctx.Value(turnUsageKey).(*turnUsage)
	return u
}

func usageFromContext(ctx context.Context) *turnUsage {
	if ts := turnStateFromContext(ctx); ts != nil && ts.usage != nil {
		return ts.usage
	}
	return turnUsageFromContext(ctx)
}

func withBackgroundTracking(ctx context.Context) context.Context {
	if u := usageFromContext(ctx); u != nil {
		return backgroundwork.WithTracker(ctx, u.begin)
	}
	return ctx
}

func (u *turnUsage) begin() func() {
	if u == nil {
		return func() {}
	}
	u.mu.Lock()
	u.pending++
	if u.idle == nil {
		u.idle = make(chan struct{})
	}
	u.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			u.mu.Lock()
			defer u.mu.Unlock()
			u.pending--
			if u.pending == 0 && u.idle != nil {
				close(u.idle)
				u.idle = nil
			}
		})
	}
}

func waitForBackgroundUsage(u *turnUsage, channel string) {
	if u == nil || channel != "pico" {
		return
	}
	if !u.wait(backgroundUsageWaitTimeout) {
		logger.WarnCF("agent", "Timed out waiting for background LLM usage", map[string]any{
			"channel": channel,
			"timeout": backgroundUsageWaitTimeout.String(),
		})
	}
}

func (u *turnUsage) wait(timeout time.Duration) bool {
	if u == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		u.mu.Lock()
		idle := u.idle
		u.mu.Unlock()
		if idle == nil {
			return true
		}
		select {
		case <-idle:
		case <-timer.C:
			return false
		}
	}
}

func (u *turnUsage) add(usage *providers.UsageInfo, model string) {
	if u == nil || usage == nil {
		return
	}
	total := usage.TotalTokens
	if total == 0 {
		total = usage.PromptTokens + usage.CompletionTokens
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	u.input += usage.PromptTokens
	u.output += usage.CompletionTokens
	u.total += total
	if model != "" {
		u.model = model
	}
}

func (u *turnUsage) snapshot() *bus.TurnUsage {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.calls == 0 {
		return nil
	}
	return &bus.TurnUsage{
		InputTokens:  u.input,
		OutputTokens: u.output,
		TotalTokens:  u.total,
		Model:        u.model,
	}
}
