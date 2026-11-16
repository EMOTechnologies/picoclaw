package backgroundwork

import (
	"context"
	"testing"
)

func TestBegin_WithoutTrackerIsNoop(t *testing.T) {
	Begin(context.Background())()
}

func TestBegin_UsesTrackerFromContext(t *testing.T) {
	pending := 0
	ctx := WithTracker(context.Background(), func() func() {
		pending++
		return func() { pending-- }
	})

	done := Begin(ctx)
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
	done()
	if pending != 0 {
		t.Fatalf("pending = %d, want 0", pending)
	}
	if WithTracker(context.Background(), nil) != context.Background() {
		t.Fatal("WithTracker(nil) should return the original context")
	}
}
