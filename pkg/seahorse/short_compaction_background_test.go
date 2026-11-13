package seahorse

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/backgroundwork"
)

type jobKey struct{}

func TestCompactCondensed_TracksBackgroundWorkAndKeepsContextValues(t *testing.T) {
	ce, s, convID := newTestCompactionEngine(t)
	seed := context.Background()

	for i := 0; i < CondensedMinFanout; i++ {
		now := time.Now().UTC()
		summary, err := s.CreateSummary(seed, CreateSummaryInput{
			ConversationID: convID,
			Kind:           SummaryKindLeaf,
			Content:        "leaf summary content " + time.Now().String(),
			TokenCount:     500,
			EarliestAt:     &now,
			LatestAt:       &now,
		})
		if err != nil {
			t.Fatalf("CreateSummary %d: %v", i, err)
		}
		s.AppendContextSummary(seed, convID, summary.SummaryID)
	}
	for i := 0; i < FreshTailCount; i++ {
		m, _ := s.AddMessage(seed, convID, "user", "fresh message", 10)
		s.AppendContextMessage(seed, convID, m.ID)
	}

	var sawJob atomic.Bool
	ce.complete = func(ctx context.Context, prompt string, opts CompleteOptions) (string, error) {
		if ctx.Value(jobKey{}) == "job-1" {
			sawJob.Store(true)
		}
		return mockCompleteFn(ctx, prompt, opts)
	}

	var pending atomic.Int32
	released := make(chan struct{})
	requestCtx, cancelRequest := context.WithCancel(context.WithValue(seed, jobKey{}, "job-1"))
	ctx := backgroundwork.WithTracker(requestCtx, func() func() {
		pending.Add(1)
		return func() {
			pending.Add(-1)
			close(released)
		}
	})

	if _, err := ce.Compact(ctx, convID, CompactInput{Force: true}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	cancelRequest()

	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("condensed compaction did not release its background work")
	}
	if pending.Load() != 0 {
		t.Fatalf("pending = %d, want 0", pending.Load())
	}
	if !sawJob.Load() {
		t.Fatal("condensed compaction did not see the request context values")
	}
}
