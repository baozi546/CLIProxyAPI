package management

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRestoreChannelAccount(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	dir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, m)
	p, q := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, pool := range []string{p, q, coreauth.SharedChannelPoolID} {
		if err := os.MkdirAll(h.channelPoolPath(pool), 0700); err != nil {
			t.Fatal(err)
		}
	}
	r := gin.New()
	r.POST("/p/:pool", h.RestoreChannelAccount)
	restore := func(pool, body string) (int, string) {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/p/"+pool, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	body := `{"type":"codex","email":"example@example.test","account_id":"fixture","refresh_token":"fixture-secret","disabled":false,"channel_pool_id":"forged","proxy_url":"https://invalid.test","account_concurrency":3}`
	for _, tc := range []struct{ pool, body, status string }{{p, `{}`, "invalid"}, {p, body, "imported"}, {p, body, "exists"}, {q, body, "other_pool"}, {coreauth.SharedChannelPoolID, body, "exists"}} {
		code, result := restore(tc.pool, tc.body)
		if code != 200 || result != `{"status":"`+tc.status+`"}` {
			t.Fatalf("wanted %s, got %d %s", tc.status, code, result)
		}
	}
	items := m.List()
	if len(items) != 1 {
		t.Fatalf("registered %d records", len(items))
	}
	a := items[0]
	if !a.Disabled || a.ChannelPool() != p || a.Metadata["proxy_url"] != nil || coreauth.AccountConcurrencyLimit(a) != 3 {
		t.Fatal("unexpected restored settings")
	}
	disk, err := os.ReadFile(a.Attributes["path"])
	if err != nil || !strings.Contains(string(disk), p) || strings.Contains(string(disk), "invalid.test") {
		t.Fatal("persisted backup mismatch")
	}
}
