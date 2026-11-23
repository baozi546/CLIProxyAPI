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
	for _, id := range []string{"first", "empty", ""} {
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
