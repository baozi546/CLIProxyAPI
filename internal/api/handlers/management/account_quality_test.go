package management

import (
	"context"
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
