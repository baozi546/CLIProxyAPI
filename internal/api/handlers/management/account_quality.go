package management

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type accountCheck struct {
	Sequence    int64  `json:"sequence"`
	Time        int64  `json:"time"`
	Model       string `json:"model"`
	Question    string `json:"question"`
	Result      string `json:"result"`
	Consecutive int    `json:"consecutive"`
}

func accountCheckFromAuth(a *coreauth.Auth) accountCheck {
	var check accountCheck
	raw, err := json.Marshal(a.Metadata["account_check"])
	if err == nil {
		_ = json.Unmarshal(raw, &check)
	}
	return check
}

var qualityQuestions = []struct{ prompt, answer string }{
	{"Return only the integer. What is 37 multiplied by 19?", "703"},
	{"Return only the integer. How many minutes are in 2 hours and 17 minutes?", "137"},
	{"Return only the integer. What is the median of 3, 8, 13, 18, 29?", "13"},
}

// A credential shared by two channels can be checked from both schedules.
// Serialize those probes so a second round reads the first round's result.
var qualityChecks sync.Map

// CheckAccountQuality pins a small diagnostic to one credential. Only a complete,
// scorable answer changes its quality streak. Provider errors are kept distinct.
func (h *Handler) CheckAccountQuality(c *gin.Context) {
	var input struct {
		Name     string `json:"name"`
		Pool     string `json:"channel_pool_id"`
		Question string `json:"question"`
		Answer   string `json:"answer"`
		Model    string `json:"model"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Name == "" || strings.TrimSpace(input.Model) == "" || len(input.Model) > 160 {
		c.JSON(400, gin.H{"error": "name and model required"})
		return
	}
	if h.authManager == nil || h.authManager.HomeEnabled() {
		c.JSON(409, gin.H{"error": "standalone manager required"})
		return
	}
	a, ok := h.lookupAuthFile(input.Name, "")
	if !ok || a == nil || a.Provider != "codex" || a.Disabled || a.Status == coreauth.StatusDisabled {
		c.JSON(409, gin.H{"error": "active Codex account required"})
		return
	}
	key := struct {
		handler *Handler
		id      string
	}{h, a.ID}
	for {
		done := make(chan struct{})
		wait, busy := qualityChecks.LoadOrStore(key, done)
		if !busy {
			defer func() { qualityChecks.Delete(key); close(done) }()
			break
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-wait.(chan struct{}):
		}
	}
	a, ok = h.authManager.GetByID(a.ID)
	if !ok || a == nil || a.Provider != "codex" || a.Disabled || a.Status == coreauth.StatusDisabled {
		c.JSON(409, gin.H{"error": "active Codex account required"})
		return
	}
	if input.Pool != "" && input.Pool != coreauth.SharedChannelPoolID && input.Pool != a.ChannelPool() {
		c.JSON(409, gin.H{"error": "account pool changed"})
		return
	}
	prompt, answer := strings.TrimSpace(input.Question), strings.TrimSpace(input.Answer)
	if (prompt == "") != (answer == "") || len(prompt) > 2048 || len(answer) > 256 {
		c.JSON(400, gin.H{"error": "invalid question or answer"})
		return
	}
	prev := accountCheckFromAuth(a)
	check := accountCheck{Time: time.Now().Unix(), Sequence: prev.Sequence + 1, Model: input.Model}
	q := qualityQuestions[uint64(prev.Sequence)%uint64(len(qualityQuestions))]
	if prompt != "" {
		q.prompt, q.answer = prompt, answer
	}
	check.Question = q.prompt
	if a.RequiresSignIn {
		check.Result = "login_required"
	} else {
		payload, _ := json.Marshal(gin.H{"model": input.Model, "messages": []gin.H{{"role": "user", "content": q.prompt}}, "stream": false})
		resp, err := h.authManager.Execute(coreauth.WithChannelPool(c.Request.Context(), a.ChannelPool()), []string{a.Provider}, exec.Request{Model: input.Model, Payload: payload}, exec.Options{SourceFormat: translator.FormatOpenAI, ResponseFormat: translator.FormatOpenAI, OriginalRequest: payload, Metadata: map[string]any{exec.PinnedAuthMetadataKey: a.ID}})
		if c.Request.Context().Err() != nil {
			return
		}
		check.Result = "inconclusive"
		check.Consecutive = prev.Consecutive
		if err == nil && !resp.Incomplete {
			var chat struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
					Finish string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal(resp.Payload, &chat) == nil && len(chat.Choices) == 1 && chat.Choices[0].Finish == "stop" && strings.TrimSpace(chat.Choices[0].Message.Content) != "" {
				check.Result = "passed"
				check.Consecutive = 0
				if strings.TrimSpace(chat.Choices[0].Message.Content) != q.answer {
					check.Result = "wrong_answer"
					check.Consecutive = prev.Consecutive + 1
				}
			}
		} else if err != nil {
			check.Result = qualityFailureResult(err)
		}
	}
	stored, committed, err := h.authManager.Modify(c.Request.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
		if current.ChannelPool() != a.ChannelPool() || current.Disabled || current.CredentialVersion != a.CredentialVersion || current.AccountPool() != a.AccountPool() || accountCheckFromAuth(current).Sequence != prev.Sequence {
			return errors.New("account changed during check")
		}
		if current.RequiresSignIn && check.Result != "account_banned" {
			check.Result = "login_required"
		} else if check.Result == "login_required" && a.RequiresSignIn {
			return errors.New("account reauthorized during check")
		}
		if current.Metadata == nil {
			current.Metadata = make(map[string]any)
		}
		current.Metadata["account_check"] = check
		if check.Result == "login_required" || check.Result == "account_banned" {
			applyAuthDisabledState(current, true)
			current.StatusMessage = "automatically disabled after account quality check: " + check.Result
		}
		return nil
	})
	if err != nil || !committed || stored == nil {
		c.JSON(409, gin.H{"error": "account changed or result could not be saved"})
		return
	}
	if stored.Disabled && h.postAuthPersistHook != nil {
		if err := h.postAuthPersistHook(c.Request.Context(), stored); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "account disabled; runtime synchronization failed"})
			return
		}
	}
	c.JSON(200, gin.H{"check": check, "disabled": stored.Disabled})
}

func qualityFailureResult(err error) string {
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) {
		return "inconclusive"
	}
	code := ""
	message := err.Error()
	var upstream *coreauth.Error
	if errors.As(err, &upstream) {
		code = upstream.Code
		message = upstream.Message
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(message), &body) == nil {
		if body.Error.Code != "" {
			code = body.Error.Code
		} else if body.Error.Type != "" {
			code = body.Error.Type
		}
	}
	if status.StatusCode() == http.StatusForbidden || status.StatusCode() == http.StatusUnauthorized {
		switch code {
		case "account_deactivated", "account_suspended", "account_banned", "user_deactivated":
			return "account_banned"
		}
	}
	if status.StatusCode() == http.StatusUnauthorized || coreauth.IsTerminalAuthError(err) || code == "invalid_grant" {
		return "login_required"
	}
	if status.StatusCode() == http.StatusTooManyRequests {
		return "rate_limited"
	}
	return "inconclusive"
}
