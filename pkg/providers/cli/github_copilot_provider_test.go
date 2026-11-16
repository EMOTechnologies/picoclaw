package cliprovider

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func TestCopilotUsageCollector_SumsAssistantUsageEvents(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	c := &copilotUsageCollector{}

	c.handle(copilot.SessionEvent{Type: copilot.SessionEventTypeAssistantMessage})
	if got := c.info(); got != nil {
		t.Fatalf("info() before any usage event = %#v, want nil", got)
	}

	c.handle(copilot.SessionEvent{
		Type: copilot.SessionEventTypeAssistantUsage,
		Data: copilot.Data{InputTokens: f(100), OutputTokens: f(20), CacheReadTokens: f(80)},
	})
	c.handle(copilot.SessionEvent{
		Type: copilot.SessionEventTypeAssistantUsage,
		Data: copilot.Data{InputTokens: f(50), OutputTokens: f(5)},
	})

	got := c.info()
	if got == nil || got.PromptTokens != 150 || got.CompletionTokens != 25 || got.TotalTokens != 175 {
		t.Fatalf("info() = %#v, want prompt=150 completion=25 total=175", got)
	}
}
