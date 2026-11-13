package runtime

import (
	"context"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

type cancelStore interface {
	RequestCancel(context.Context, domain.RunID) error
	Cancelled(context.Context, domain.RunID) (bool, error)
	ClearCancel(context.Context, domain.RunID) error
}

func (r *Runtime) watchCancel(ctx context.Context, id domain.RunID, cancel context.CancelFunc) error {
	store, ok := r.config.Store.(cancelStore)
	if !ok {
		return nil
	}
	if err := store.ClearCancel(ctx, id); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cancelled, err := store.Cancelled(ctx, id)
				if err != nil || cancelled {
					cancel()
					return
				}
			}
		}
	}()
	return nil
}
