package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// AccountConcurrencyLimit is zero for existing credentials without a ceiling.
func AccountConcurrencyLimit(a *Auth) int {
	if a == nil {
		return 0
	}
	n, err := strconv.Atoi(fmt.Sprint(a.Metadata["account_concurrency"]))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// ReadPoolAccountLimit returns nil until a pool-wide per-account limit is saved.
// Existing credentials keep their own settings until that first edit.
func ReadPoolAccountLimit(dir, pool string) (*int, error) {
	if dir == "" || len(pool) != 32 {
		return nil, nil
	}
	if _, err := hex.DecodeString(pool); err != nil {
		return nil, nil
	}
	b, err := os.ReadFile(filepath.Join(dir, ".channel-pools", pool, "account-limit"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 || n > 1000000 {
		return nil, errors.New("invalid pool account limit")
	}
	return &n, nil
}

type accountSlots struct {
	sync.Mutex
	active map[string]*accountSlot
}
type accountSlot struct {
	count   int
	changed chan struct{}
}

// Admission is local to a standalone CPA instance. Home owns its own leases.
func (m *Manager) acquireAccount(ctx context.Context, a *Auth) (func(), error) {
	if m.HomeEnabled() || a == nil {
		return func() {}, nil
	}
	slots := &m.accountSlots
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var override *int
		if cfg := m.runtimeConfigSnapshot(); cfg != nil && a.ChannelPool() != "" {
			var err error
			override, err = ReadPoolAccountLimit(cfg.AuthDir, a.ChannelPool())
			if err != nil {
				return nil, &Error{Code: "pool_policy_unavailable", Message: "account limit unavailable", HTTPStatus: 503}
			}
		}
		slots.Lock()
		m.mu.RLock()
		current := m.auths[a.ID]
		if current == nil || current.Disabled || current.Status == StatusDisabled || current.RegistrationEpoch != a.RegistrationEpoch || current.ChannelPool() != ChannelPoolFromContext(ctx) {
			m.mu.RUnlock()
			slots.Unlock()
			return nil, &Error{Code: "credential_unavailable", Message: "credential changed while waiting for admission", HTTPStatus: 409}
		}
		limit := AccountConcurrencyLimit(current)
		if override != nil {
			limit = *override
		}
		m.mu.RUnlock()
		if slots.active == nil {
			slots.active = make(map[string]*accountSlot)
		}
		s := slots.active[a.ID]
		if s == nil {
			s = &accountSlot{changed: make(chan struct{})}
			slots.active[a.ID] = s
		}
		if limit == 0 || s.count < limit {
			s.count++
			slots.Unlock()
			return func() {
				slots.Lock()
				defer slots.Unlock()
				s.count--
				close(s.changed)
				if s.count == 0 {
					delete(slots.active, a.ID)
				} else {
					s.changed = make(chan struct{})
				}
			}, nil
		}
		changed := s.changed
		slots.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		case <-time.After(time.Second): // Re-read limits and disabled state after an operator edit.
		}
	}
}

func (m *Manager) runAccount(ctx context.Context, a *Auth, req exec.Request, opts exec.Options, call func(context.Context, *Auth, exec.Request, exec.Options) (exec.Response, error)) (exec.Response, error) {
	release, err := m.acquireAccount(ctx, a)
	if err != nil {
		return exec.Response{}, err
	}
	defer release()
	return call(ctx, a, req, opts)
}
