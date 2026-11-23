package api

import (
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *Server) channelPoolScope(c *gin.Context) {
	id := c.Param("pool")
	if s.mgmt == nil || !s.mgmt.HasChannelPool(id) {
		c.AbortWithStatusJSON(404, gin.H{"error": gin.H{"message": "channel pool not found"}})
		return
	}
	if s.handlers.AuthManager == nil || s.handlers.AuthManager.HomeEnabled() {
		c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"message": "channel pools require standalone CPA"}})
		return
	}
	c.Request = c.Request.WithContext(auth.WithChannelPool(c.Request.Context(), id))
	if c.Request.Method != http.MethodGet {
		for _, member := range s.handlers.AuthManager.List() {
			if member.ChannelPool() == id && !member.Disabled && member.Status != auth.StatusDisabled {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"code": "channel_pool_empty", "message": "No active accounts in this channel pool"}})
		return
	}
	c.Next()
}

func (s *Server) channelPoolModels(c *gin.Context) {
	ids := map[string]bool{}
	pool := c.Param("pool")
	for _, a := range s.handlers.AuthManager.List() {
		if a.Disabled || a.Status == auth.StatusDisabled || a.ChannelPool() != pool {
			continue
		}
		for _, model := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
			if model != nil {
				ids[model.ID] = true
			}
		}
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	data := make([]gin.H, 0, len(names))
	for _, id := range names {
		data = append(data, gin.H{"id": id, "object": "model", "owned_by": "codex"})
	}
	c.JSON(200, gin.H{"object": "list", "data": data})
}
