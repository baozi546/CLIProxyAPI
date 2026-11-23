package auth

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
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
