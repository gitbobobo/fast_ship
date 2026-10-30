package service

import (
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
)

func TestDbgGateAuth(t *testing.T) {
	env := newReviewEngineEnv(t)
	env.enableReview(t)
	base := redeliveryTestNow.Add(-time.Hour)
	env.fake.SetHookDeliveries([]githubapp.HookDelivery{
		hookRow(3105, "a-unauth", "issue_comment", "created", 500, false, base),
		hookRow(3106, "b-auth", "issue_comment", "created", 500, false, base),
	})
	env.fake.SetHookDeliveryPayload(3105, redeliveryCommentPayload("owner/repo", 10, "NONE"))
	env.fake.SetHookDeliveryPayload(3106, redeliveryCommentPayload("owner/repo", 10, "COLLABORATOR"))
	env.fake.SetPR("owner/repo", 10, &githubapi.PullRequest{})

	env.review.reconcileWebhookDeliveries(redeliveryTestNow)
	t.Logf("redelivered=%v", env.fake.RedeliveredIDs())
	for _, g := range []string{"a-unauth", "b-auth"} {
		row, ok := redeliveryClosedRow(t, env, g)
		t.Logf("%s closed-at=%v present=%v", g, row.ClosedAt, ok)
	}
}
