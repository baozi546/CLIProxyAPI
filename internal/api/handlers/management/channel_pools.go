package management

import (
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"

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
