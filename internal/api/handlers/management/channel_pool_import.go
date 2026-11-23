package management

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// Restore only the native OAuth fields and account settings. Operational routing,
// proxy overrides and the destination pool are controlled by the receiving site.
type poolBackup struct {
	Type        string `json:"type"`
	Email       string `json:"email"`
	AccountID   string `json:"account_id"`
	Access      string `json:"access_token,omitempty"`
	Refresh     string `json:"refresh_token,omitempty"`
	IDToken     string `json:"id_token,omitempty"`
	Expired     string `json:"expired,omitempty"`
	LastRefresh string `json:"last_refresh,omitempty"`
	Plan        string `json:"plan_type,omitempty"`
	SIWC        bool   `json:"use_siwc,omitempty"`
	Category    string `json:"account_pool,omitempty"`
	Limit       int    `json:"account_concurrency,omitempty"`
	Disabled    bool   `json:"disabled"`
	ChannelPool string `json:"channel_pool_id"`
}

func (h *Handler) RestoreChannelAccount(c *gin.Context) {
	pool := c.Param("pool")
	if h.authManager == nil || !h.HasChannelPool(pool) {
		c.JSON(400, gin.H{"status": "unknown_pool"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var b poolBackup
	if c.ShouldBindJSON(&b) != nil {
		c.JSON(200, gin.H{"status": "invalid"})
		return
	}
	b.Email = strings.TrimSpace(b.Email)
	b.AccountID = strings.TrimSpace(b.AccountID)
	address, err := mail.ParseAddress(b.Email)
	validEmail := err == nil && address.Address == b.Email && len(b.Email) <= 254 && !strings.ContainsAny(b.Email, `/\:`)
	validIdentity := b.Type == "codex" && !b.SIWC && b.AccountID != "" && len(b.AccountID) <= 256
	hasCredentials := strings.TrimSpace(b.Access) != "" || strings.TrimSpace(b.Refresh) != ""
	validSettings := (b.Category == "" || validAccountPool(b.Category)) && b.Limit >= 0 && b.Limit <= 1000000
	if !validEmail || !validIdentity || !hasCredentials || !validSettings {
		c.JSON(200, gin.H{"status": "invalid"})
		return
	}
	b.Disabled = true
	b.ChannelPool = pool
	digest := sha256.Sum256([]byte(b.AccountID))
	name := codex.CredentialFileName(b.Email, b.Plan, hex.EncodeToString(digest[:4]), true)
	if len(name) > 240 || isUnsafeAuthFileName(name) {
		c.JSON(200, gin.H{"status": "invalid"})
		return
	}
	data, err := json.Marshal(b)
	if err != nil {
		c.JSON(500, gin.H{"status": "failed"})
		return
	}
	h.codexPoolSaveMu.Lock()
	defer h.codexPoolSaveMu.Unlock()
	h.v8AuthUploadMu.Lock()
	defer h.v8AuthUploadMu.Unlock()
	for _, a := range h.authManager.List() {
		if a.Provider != "codex" {
			continue
		}
		email, id := authIdentityEmailAndAccountID(a)
		if (id == b.AccountID && (email == "" || strings.EqualFold(email, b.Email))) || (id == "" && strings.EqualFold(email, b.Email)) {
			status := "exists"
			if a.ChannelPool() != pool {
				status = "other_pool"
			}
			c.JSON(200, gin.H{"status": status})
			return
		}
	}
	dst := filepath.Join(h.currentHandlerAuthDir(), name)
	if _, err = os.Lstat(dst); err == nil {
		c.JSON(200, gin.H{"status": "exists"})
		return
	}
	if err = h.writeAuthFileV8Locked(c.Request.Context(), name, data, pool); errors.Is(err, os.ErrExist) {
		c.JSON(200, gin.H{"status": "exists"})
		return
	} else if err != nil {
		c.JSON(500, gin.H{"status": "failed"})
		return
	}
	c.JSON(200, gin.H{"status": "imported"})
}
