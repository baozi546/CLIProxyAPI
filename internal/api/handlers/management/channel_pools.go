package management

import (
	"encoding/hex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
)

func validChannelPool(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && filepath.Base(id) == id
}

// Empty pools are durable as directories, without introducing credential files.
func (h *Handler) channelPoolPath(id string) string {
	cfg := h.configSnapshot()
	if !validChannelPool(id) || cfg == nil || cfg.AuthDir == "" {
		return ""
	}
	return filepath.Join(cfg.AuthDir, ".channel-pools", id)
}

func (h *Handler) HasChannelPool(id string) bool {
	path := h.channelPoolPath(id)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (h *Handler) EnsureChannelPool(c *gin.Context) {
	id := c.Param("pool")
	path := h.channelPoolPath(id)
	if path == "" {
		c.JSON(400, gin.H{"error": "invalid channel pool"})
		return
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		c.JSON(500, gin.H{"error": "could not create channel pool"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id})
}

func (h *Handler) PoolAccountLimit(c *gin.Context) {
	pool := c.Param("pool")
	if !h.HasChannelPool(pool) {
		c.JSON(404, gin.H{"error": "unknown pool"})
		return
	}
	if c.Request.Method == http.MethodGet {
		n, err := coreauth.ReadPoolAccountLimit(h.configSnapshot().AuthDir, pool)
		if err != nil {
			c.JSON(500, gin.H{"error": "could not read pool limit"})
			return
		}
		c.JSON(200, gin.H{"limit": n})
		return
	}
	var input struct {
		Limit *int `json:"limit"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Limit == nil || *input.Limit < 0 || *input.Limit > 1000000 {
		c.JSON(400, gin.H{"error": "invalid limit"})
		return
	}
	dir := h.channelPoolPath(pool)
	tmp, err := os.CreateTemp(dir, ".limit-*")
	if err != nil {
		c.JSON(500, gin.H{"error": "could not write pool limit"})
		return
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(strconv.Itoa(*input.Limit))
	closeErr := tmp.Close()
	if err != nil || closeErr != nil || os.Rename(tmp.Name(), filepath.Join(dir, "account-limit")) != nil {
		c.JSON(500, gin.H{"error": "could not write pool limit"})
		return
	}
	c.JSON(200, gin.H{"limit": *input.Limit})
}
