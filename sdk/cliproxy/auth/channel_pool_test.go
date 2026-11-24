package auth

import (
	"errors"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"testing"
)

func TestChannelPoolNormalStreamAndFallback(t *testing.T) {
	a, b := newIssue7521Auth("dedicated-a"), newIssue7521Auth("dedicated-b")
	a.Metadata = map[string]any{"channel_pool_id": "first"}
	b.Metadata = map[string]any{"channel_pool_id": "second"}
	executor := &authFallbackExecutor{id: "claude", executeErrors: map[string]error{a.ID: errors.New("fixture upstream failed")}}
	manager := newIssue7521Manager(t, nil, executor, a, b)
	manager.SetRetryConfig(0, 0, 0)
	req := exec.Request{Model: issue7521PoolModel}
	providers := []string{"claude"}
	for _, id := range []string{"first", "empty"} {
		if _, err := manager.Execute(WithChannelPool(t.Context(), id), providers, req, exec.Options{}); err == nil {
			t.Fatalf("unexpected success in pool %q", id)
		}
	}
	calls := executor.ExecuteCalls()
	if len(calls) != 1 || calls[0] != a.ID {
		t.Fatalf("cross-pool retry: %v", calls)
	}
	ctx := WithChannelPool(t.Context(), "second")
	res, err := manager.Execute(ctx, providers, req, exec.Options{})
	if err != nil || string(res.Payload) != b.ID {
		t.Fatalf("second pool: %v %s", err, res.Payload)
	}
	stream, err := manager.ExecuteStream(ctx, providers, req, exec.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for chunk := range stream.Chunks {
		if string(chunk.Payload) != b.ID {
			t.Fatalf("wrong streaming credential: %s", chunk.Payload)
		}
	}
	if _, err := manager.Execute(ctx, providers, req, exec.Options{Metadata: map[string]any{exec.PinnedAuthMetadataKey: a.ID}}); err == nil {
		t.Fatal("cross-pool pin accepted")
	}
}

func TestDefaultEndpointSharesAccountsAcrossLegacyPools(t *testing.T) {
	plus, pro := newIssue7521Auth("plus"), newIssue7521Auth("pro")
	plus.Metadata = map[string]any{"channel_pool_id": "legacy-plus", "account_pool": "plus"}
	pro.Metadata = map[string]any{"channel_pool_id": "legacy-pro", "account_pool": "pro"}
	executor := &authFallbackExecutor{id: "claude"}
	manager := newIssue7521Manager(t, nil, executor, plus, pro)
	req := exec.Request{Model: issue7521PoolModel}
	for _, pool := range []string{"", "00000000000000000000000000000000"} {
		ctx := WithAccountPool(WithChannelPool(t.Context(), pool), "pro")
		res, err := manager.Execute(ctx, []string{"claude"}, req, exec.Options{})
		if err != nil || string(res.Payload) != pro.ID {
			t.Fatalf("shared endpoint %q: %v %s", pool, err, res.Payload)
		}
		stream, err := manager.ExecuteStream(ctx, []string{"claude"}, req, exec.Options{})
		if err != nil {
			t.Fatal(err)
		}
		for chunk := range stream.Chunks {
			if string(chunk.Payload) != pro.ID {
				t.Fatalf("wrong shared endpoint category: %s", chunk.Payload)
			}
		}
		if _, err := manager.Execute(ctx, []string{"claude"}, req, exec.Options{Metadata: map[string]any{exec.PinnedAuthMetadataKey: plus.ID}}); err == nil {
			t.Fatal("shared endpoint accepted a cross-category pin")
		}
	}
}

func TestSharedChannelPoolSelectsOnlyRequestedAccountCategory(t *testing.T) {
	plus, pro, degraded := newIssue7521Auth("plus"), newIssue7521Auth("pro"), newIssue7521Auth("degraded")
	plus.Metadata = map[string]any{"channel_pool_id": "shared", "account_pool": "pro"}
	plus.Attributes = map[string]string{"plan_type": "plus"}
	pro.Metadata = map[string]any{"channel_pool_id": "shared", "account_pool": "pro"}
	degraded.Metadata = map[string]any{"channel_pool_id": "shared", "account_pool": "degraded", "plan_type": "pro"}
	executor := &authFallbackExecutor{id: "claude"}
	manager := newIssue7521Manager(t, nil, executor, plus, pro, degraded)
	manager.SetRetryConfig(0, 0, 0)
	req := exec.Request{Model: issue7521PoolModel}
	for _, category := range []string{"plus", "pro", "degraded", "unassigned"} {
		ctx := WithAccountPool(WithChannelPool(t.Context(), "shared"), category)
		res, err := manager.Execute(ctx, []string{"claude"}, req, exec.Options{})
		if category == "unassigned" {
			if err == nil {
				t.Fatal("empty category fell back to another subscription")
			}
			continue
		}
		if err != nil || string(res.Payload) != category {
			t.Fatalf("category %s: %v %s", category, err, res.Payload)
		}
		stream, err := manager.ExecuteStream(ctx, []string{"claude"}, req, exec.Options{})
		if err != nil {
			t.Fatal(err)
		}
		for chunk := range stream.Chunks {
			if string(chunk.Payload) != category {
				t.Fatalf("wrong streaming category: %s", chunk.Payload)
			}
		}
	}
	ctx := WithAccountPool(WithChannelPool(t.Context(), "shared"), "plus")
	if _, err := manager.Execute(ctx, []string{"claude"}, req, exec.Options{Metadata: map[string]any{exec.PinnedAuthMetadataKey: pro.ID}}); err == nil {
		t.Fatal("cross-category pin accepted")
	}
	executor.executeErrors = map[string]error{plus.ID: errors.New("fixture failed")}
	if _, err := manager.Execute(ctx, []string{"claude"}, req, exec.Options{}); err == nil {
		t.Fatal("cross-category fallback accepted")
	}
}
