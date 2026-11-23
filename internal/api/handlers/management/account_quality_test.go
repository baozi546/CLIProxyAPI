package management

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAccountCheckRequiresSignInDisablesOnlyTarget(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"needs-login", "healthy"} {
		_, err := manager.Register(t.Context(), &coreauth.Auth{ID: id, FileName: id, Provider: "codex", Status: coreauth.StatusActive, RequiresSignIn: id == "needs-login"})
		if err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	var synced string
	h.SetPostAuthPersistHook(func(_ context.Context, a *coreauth.Auth) error { synced = a.ID; return nil })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v8/management/credentials/quality-check", strings.NewReader(`{"name":"needs-login","model":"fixture"}`))
	h.CheckAccountQuality(c)
	target, _ := manager.GetByID("needs-login")
	other, _ := manager.GetByID("healthy")
	if w.Code != 200 || !target.Disabled || other.Disabled || synced != "needs-login" || accountCheckFromAuth(target).Result != "login_required" {
		t.Fatalf("unexpected state: HTTP %d, synchronized %q, response %s", w.Code, synced, w.Body.String())
	}
}

func TestQualityCheckRejectsMovedAccountAndUsesChannelQuestion(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	_, err := m.Register(t.Context(), &coreauth.Auth{ID: "test-id", FileName: "test-id", Provider: "codex", RequiresSignIn: true, Metadata: map[string]any{"channel_pool_id": "own"}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)
	for _, tc := range []struct {
		pool, answer string
		code         int
	}{{"other", "4", 409}, {"own", "", 400}, {"own", "4", 200}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/checks", strings.NewReader(`{"name":"test-id","model":"fixture","question":"2+2?","answer":"`+tc.answer+`","channel_pool_id":"`+tc.pool+`"}`))
		h.CheckAccountQuality(c)
		if w.Code != tc.code {
			t.Fatalf("status %d", w.Code)
		}
	}
	a, _ := m.GetByID("test-id")
	if check := accountCheckFromAuth(a); check.Question != "2+2?" || check.Result != "login_required" {
		t.Fatal(check)
	}
}

type qualityFailureExecutor struct {
	failingRefreshManagementExecutor
	failure error
}

func (e *qualityFailureExecutor) Execute(context.Context, *coreauth.Auth, exec.Request, exec.Options) (exec.Response, error) {
	return exec.Response{}, e.failure
}

func TestQualityCheckDisablesConfirmedAuthFailuresAndRetainsTemporaryFailures(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          int
		message, result string
		disabled        bool
	}{
		{"banned", 403, `{"error":{"code":"account_deactivated"}}`, "account_banned", true},
		{"invalid", 401, `{"error":{"code":"invalid_api_key"}}`, "login_required", true},
		{"limited", 429, "rate limited", "rate_limited", false},
		{"forbidden", 403, "request forbidden by policy", "inconclusive", false},
		{"unavailable", 503, "upstream unavailable", "inconclusive", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			m.SetRetryConfig(0, 0, 0)
			m.RegisterExecutor(&qualityFailureExecutor{failingRefreshManagementExecutor: failingRefreshManagementExecutor{provider: "codex"}, failure: &coreauth.Error{Code: "upstream_error", HTTPStatus: tc.status, Message: tc.message}})
			for _, id := range []string{"target", "other"} {
				if _, err := m.Register(t.Context(), &coreauth.Auth{ID: id, FileName: id, Provider: "codex", Status: coreauth.StatusActive}); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "fixture"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, m)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/checks", strings.NewReader(`{"name":"target","model":"fixture","question":"2+2?","answer":"4"}`))
			h.CheckAccountQuality(c)
			target, _ := m.GetByID("target")
			other, _ := m.GetByID("other")
			if w.Code != 200 || target.Disabled != tc.disabled || other.Disabled || accountCheckFromAuth(target).Result != tc.result {
				t.Fatalf("status=%d disabled=%v other=%v result=%s body=%s", w.Code, target.Disabled, other.Disabled, accountCheckFromAuth(target).Result, w.Body.String())
			}
		})
	}
}
