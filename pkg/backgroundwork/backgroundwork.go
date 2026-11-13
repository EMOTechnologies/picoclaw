package backgroundwork

import "context"

type trackerKey struct{}

func WithTracker(ctx context.Context, begin func() func()) context.Context {
	if begin == nil {
		return ctx
	}
	return context.WithValue(ctx, trackerKey{}, begin)
}

func Begin(ctx context.Context) func() {
	if ctx != nil {
		if begin, ok := ctx.Value(trackerKey{}).(func() func()); ok {
			return begin()
		}
	}
	return func() {}
}
