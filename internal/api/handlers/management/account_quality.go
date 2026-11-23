package management

import (
	"errors"
	"net/http"
	"strings"
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

// CheckAccountQuality pins a small diagnostic to one credential. Only a complete,
// scorable answer changes its quality streak. Provider errors are kept distinct.
func (h *Handler) CheckAccountQuality(c *gin.Context) {
	var input struct {
		Name  string `json:"name"`
		Model string `json:"model"`
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
	prev := accountCheckFromAuth(a)
	check := accountCheck{Time: time.Now().Unix(), Sequence: prev.Sequence + 1, Model: input.Model}
	q := qualityQuestions[uint64(prev.Sequence)%uint64(len(qualityQuestions))]
	check.Question = q.prompt
	if a.RequiresSignIn {
		check.Result = "login_required"
	} else {
		payload, _ := json.Marshal(gin.H{"model": input.Model, "messages": []gin.H{{"role": "user", "content": q.prompt}}, "stream": false})
		resp, err := h.authManager.Execute(c.Request.Context(), []string{a.Provider}, exec.Request{Model: input.Model, Payload: payload}, exec.Options{SourceFormat: translator.FormatOpenAI, ResponseFormat: translator.FormatOpenAI, OriginalRequest: payload, Metadata: map[string]any{exec.PinnedAuthMetadataKey: a.ID}})
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
			var status interface{ StatusCode() int }
			if errors.As(err, &status) && status.StatusCode() == http.StatusTooManyRequests {
				check.Result = "rate_limited"
			}
		}
	}
	stored, committed, err := h.authManager.Modify(c.Request.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
		if current.Disabled || current.CredentialVersion != a.CredentialVersion || accountCheckFromAuth(current).Sequence != prev.Sequence {
			return errors.New("account changed during check")
		}
		if current.RequiresSignIn {
			check.Result = "login_required"
		} else if check.Result == "login_required" {
			return errors.New("account reauthorized during check")
		}
		if current.Metadata == nil {
			current.Metadata = make(map[string]any)
		}
		current.Metadata["account_check"] = check
		if check.Result == "login_required" {
			applyAuthDisabledState(current, true)
		}
		return nil
	})
	if err != nil || !committed || stored == nil {
		c.JSON(409, gin.H{"error": "account changed or result could not be saved"})
		return
	}
	c.JSON(200, gin.H{"check": check, "disabled": stored.Disabled})
}
