package auth

import (
	"context"
	"errors"
	cfg "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestAccountLimitQueuesUntilRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := &Auth{ID: "a", Metadata: map[string]any{"account_concurrency": 1}}
		b := &Auth{ID: "b", Metadata: map[string]any{"account_concurrency": 1}}
		m := &Manager{auths: map[string]*Auth{"a": a, "b": b}}
		release, err := m.acquireAccount(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
		other, err := m.acquireAccount(t.Context(), b)
		if err != nil {
			t.Fatal(err)
		}
		other()
		done := make(chan struct{})
		go func() {
			defer close(done)
			free, err := m.acquireAccount(t.Context(), a)
			if err != nil {
				t.Error(err)
				return
			}
			free()
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("admitted a second request before release")
		default:
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := m.acquireAccount(ctx, a); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		release()
		<-done
		if len(m.accountSlots.active) != 0 {
			t.Fatal("leaked account slots")
		}
	})
}

func TestPoolLimitAppliesPerAccountAndSurvivesManagerRestart(t *testing.T) {
	dir := t.TempDir()
	pool := strings.Repeat("a", 32)
	path := filepath.Join(dir, ".channel-pools", pool)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "account-limit"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		m := NewManager(nil, nil, nil)
		m.SetConfig(&cfg.Config{AuthDir: dir})
		ctx := WithChannelPool(t.Context(), pool)
		var frees []func()
		for _, id := range []string{"already-present", "new-import"} {
			a, err := m.Register(ctx, &Auth{ID: id, Provider: "codex", Metadata: map[string]any{"channel_pool_id": pool, "account_concurrency": 9}})
			if err != nil {
				t.Fatal(err)
			}
			free, err := m.acquireAccount(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			frees = append(frees, free)
			wait, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
			_, err = m.acquireAccount(wait, a)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("second request: %v", err)
			}
		}
		for _, free := range frees {
			free()
		}
	}
}

func TestSharedPoolLimitAppliesToLegacyAccountsThroughEveryEndpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".channel-pools", SharedChannelPoolID)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "account-limit"), []byte("1"), 0600); err != nil {
			t.Fatal(err)
		}
		m := NewManager(nil, nil, nil)
		m.SetConfig(&cfg.Config{AuthDir: dir})
		a, err := m.Register(t.Context(), &Auth{ID: "legacy", Provider: "codex", Metadata: map[string]any{"channel_pool_id": "legacy", "account_concurrency": 9}})
		if err != nil {
			t.Fatal(err)
		}
		for _, pool := range []string{"", SharedChannelPoolID, "legacy"} {
			ctx := WithChannelPool(t.Context(), pool)
			free, err := m.acquireAccount(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			waiting, cancel := context.WithTimeout(ctx, time.Second)
			_, err = m.acquireAccount(waiting, a)
			cancel()
			free()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pool %q exceeded the shared account limit: %v", pool, err)
			}
		}
	})
}
