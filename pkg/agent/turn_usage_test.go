package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestTurnUsage_AddAndSnapshot(t *testing.T) {
	var nilUsage *turnUsage
	nilUsage.add(&providers.UsageInfo{PromptTokens: 1}, "m")
	if got := nilUsage.snapshot(); got != nil {
		t.Fatalf("nil snapshot = %+v, want nil", got)
	}

	u := &turnUsage{}
	if got := u.snapshot(); got != nil {
		t.Fatalf("empty snapshot = %+v, want nil", got)
	}

	u.add(nil, "ignored")
	u.add(&providers.UsageInfo{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}, "model-a")
	u.add(&providers.UsageInfo{PromptTokens: 200, CompletionTokens: 20}, "model-b")
	u.add(&providers.UsageInfo{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, "")

	want := bus.TurnUsage{InputTokens: 301, OutputTokens: 31, TotalTokens: 332, Model: "model-b"}
	if got := u.snapshot(); got == nil || *got != want {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
}

func TestTurnUsage_ContextReusesExistingAccumulator(t *testing.T) {
	ctx := withTurnUsage(context.Background())
	first := turnUsageFromContext(ctx)
	if first == nil {
		t.Fatal("expected accumulator in context")
	}
	if again := turnUsageFromContext(withTurnUsage(ctx)); again != first {
		t.Fatal("withTurnUsage replaced an existing accumulator")
	}
	if turnUsageFromContext(context.Background()) != nil {
		t.Fatal("expected no accumulator in bare context")
	}
}

type usageScriptedProvider struct {
	calls int
}

func (m *usageScriptedProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{ID: "call-1", Name: "mock_custom", Arguments: map[string]any{"task": "ping"}},
			},
			Usage: &providers.UsageInfo{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
		}, nil
	}
	return &providers.LLMResponse{
		Content: "done",
		Usage:   &providers.UsageInfo{PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220},
	}, nil
}

func (m *usageScriptedProvider) GetDefaultModel() string {
	return "usage-model"
}

func TestProcessMessageSync_PublishesAccumulatedTurnUsage(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	provider := &usageScriptedProvider{}
	al := NewAgentLoop(cfg, msgBus, provider)
	al.RegisterTool(&mockCustomTool{})

	al.processMessageSync(context.Background(), testInboundMessage(bus.InboundMessage{
		Channel:  "cli",
		SenderID: "user1",
		ChatID:   "direct",
		Content:  "run tool",
	}))

	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case outbound := <-msgBus.OutboundChan():
			if outbound.Content != "done" {
				continue
			}
			if outbound.TurnUsage == nil {
				t.Fatal("final outbound message has no TurnUsage")
			}
			got := *outbound.TurnUsage
			if got.InputTokens != 300 || got.OutputTokens != 30 || got.TotalTokens != 330 {
				t.Fatalf("TurnUsage = %+v, want input=300 output=30 total=330", got)
			}
			if got.Model == "" {
				t.Fatal("TurnUsage.Model is empty")
			}
			return
		case <-deadline:
			t.Fatal("final outbound message was not published")
		}
	}
}

func TestTurnUsage_WaitTracksBackgroundWork(t *testing.T) {
	u := &turnUsage{}
	if !u.wait(10 * time.Millisecond) {
		t.Fatal("wait() with no pending work should return true")
	}

	done := u.begin()
	if u.wait(10 * time.Millisecond) {
		t.Fatal("wait() should time out while work is pending")
	}
	done()
	done()
	if !u.wait(10 * time.Millisecond) {
		t.Fatal("wait() should return true after work finished")
	}

	var nilUsage *turnUsage
	nilUsage.begin()()
	if !nilUsage.wait(time.Millisecond) {
		t.Fatal("nil wait() should return true")
	}
}

func TestPublishResponseIfNeeded_PicoWaitsForBackgroundUsage(t *testing.T) {
	al, _, msgBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	ctx := withTurnUsage(context.Background())
	u := turnUsageFromContext(ctx)
	u.add(&providers.UsageInfo{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}, "main-model")
	done := u.begin()
	go func() {
		time.Sleep(50 * time.Millisecond)
		u.add(&providers.UsageInfo{PromptTokens: 40, CompletionTokens: 4, TotalTokens: 44}, "summary-model")
		done()
	}()

	al.PublishResponseIfNeeded(ctx, "pico", "pico:sess-1", "", "final reply")

	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.TurnUsage == nil || outbound.TurnUsage.TotalTokens != 154 {
			t.Fatalf("TurnUsage = %+v, want total 154 including background work", outbound.TurnUsage)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("final reply was not published")
	}
}

type usageSummaryProvider struct {
	calls atomic.Int32
}

func (p *usageSummaryProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	return &providers.LLMResponse{
		Content: "summary text",
		Usage:   &providers.UsageInfo{PromptTokens: 50, CompletionTokens: 5, TotalTokens: 55},
	}, nil
}

func (p *usageSummaryProvider) GetDefaultModel() string {
	return "summary-model"
}

func TestLegacyCompact_SummarizationUsageIsTrackedAndCounted(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:                 t.TempDir(),
				ModelName:                 "test-model",
				MaxTokens:                 4096,
				MaxToolIterations:         10,
				ContextWindow:             8000,
				SummarizeMessageThreshold: 2,
				SummarizeTokenPercent:     75,
			},
		},
	}
	provider := &usageSummaryProvider{}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defaultAgent := al.registry.GetDefaultAgent()
	defaultAgent.Sessions.SetHistory("session-1", []providers.Message{
		{Role: "user", Content: "Question one"},
		{Role: "assistant", Content: "Answer one"},
		{Role: "user", Content: "Question two"},
		{Role: "assistant", Content: "Answer two"},
		{Role: "user", Content: "Question three"},
		{Role: "assistant", Content: "Answer three"},
	})

	ctx := withTurnUsage(context.Background())
	u := turnUsageFromContext(ctx)
	lcm := &legacyContextManager{al: al}
	if err := lcm.Compact(ctx, &CompactRequest{
		SessionKey: "session-1",
		Reason:     ContextCompressReasonSummarize,
	}); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}

	if !u.wait(3 * time.Second) {
		t.Fatal("summarization was not tracked to completion")
	}
	calls := int(provider.calls.Load())
	if calls == 0 {
		t.Fatal("expected summarization LLM call")
	}
	got := u.snapshot()
	if got == nil || got.TotalTokens != 55*calls || got.InputTokens != 50*calls {
		t.Fatalf("snapshot = %+v, want %d summarization calls counted", got, calls)
	}
}

func TestSpawnSubTurn_UsageRollsIntoParentAndReleasesBackgroundWork(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
	provider := &usageSummaryProvider{}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)

	parent := &turnState{
		ctx:            context.Background(),
		turnID:         "parent-usage-1",
		pendingResults: make(chan *tools.ToolResult, 1),
		session:        &ephemeralSessionStore{},
		usage:          &turnUsage{},
	}

	if _, err := spawnSubTurn(context.Background(), al, parent, SubTurnConfig{
		Model: "gpt-4o-mini",
		Tools: []tools.Tool{},
	}); err != nil {
		t.Fatalf("spawnSubTurn() error = %v", err)
	}

	if !parent.usage.wait(10 * time.Millisecond) {
		t.Fatal("sub-turn background work was not released")
	}
	calls := int(provider.calls.Load())
	got := parent.usage.snapshot()
	if calls == 0 || got == nil || got.TotalTokens != 55*calls {
		t.Fatalf("parent usage = %+v, want %d sub-turn calls counted", got, calls)
	}
}
