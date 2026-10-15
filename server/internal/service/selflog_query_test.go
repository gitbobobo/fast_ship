package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
)

func TestSelfLogServiceRejectsEmptyRequester(t *testing.T) {
	store, err := selflog.OpenStore(filepath.Join(t.TempDir(), "self_logs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := NewSelfLogService(store)
	ctx := context.Background()

	if _, err := svc.ListSelfLogs(ctx, "", SelfLogListParams{Limit: 10}); err != errs.ErrInvalidParams {
		t.Fatalf("list empty requester = %v, want ErrInvalidParams", err)
	}
	if _, err := svc.AggregateSelfLogs(ctx, "  ", SelfLogAggregateParams{ByModule: true}); err != errs.ErrInvalidParams {
		t.Fatalf("aggregate empty requester = %v, want ErrInvalidParams", err)
	}
	if _, err := svc.TraceSelfLogs(ctx, "", "req-1", "", nil); err != errs.ErrInvalidParams {
		t.Fatalf("trace empty requester = %v, want ErrInvalidParams", err)
	}
}
