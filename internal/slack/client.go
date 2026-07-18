package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var authErrors = map[string]bool{
	"invalid_auth":      true,
	"not_authed":        true,
	"token_expired":     true,
	"token_revoked":     true,
	"account_inactive":  true,
	"org_login_required": true,
}

type AuthError struct {
	Code string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("Slack credentials rejected (%s). Re-copy a fresh curl from Chrome DevTools and update .curl.", e.Code)
}

type Client struct {
	token      string
	baseURL    string
	httpClient *http.Client
	cookies    string

	mu       sync.Mutex
	lastCall time.Time
}

func NewClient(token, cookie, workspace, rawCookies string) *Client {
	base := workspace
	if base == "" {
		base = "slack.com"
	}

	c := &Client{
		token:      token,
		baseURL:    fmt.Sprintf("https://%s/api", base),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}

	if rawCookies != "" {
		c.cookies = rawCookies
	} else if cookie != "" {
		c.cookies = "d=" + cookie
	}

	return c
}

func (c *Client) throttle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	elapsed := time.Since(c.lastCall)
	if elapsed < time.Second {
		time.Sleep(time.Second - elapsed)
	}
	c.lastCall = time.Now()
}

func (c *Client) post(method string, params map[string]string) (json.RawMessage, error) {
	for attempt := 0; attempt < 5; attempt++ {
		c.throttle()

		form := url.Values{"token": {c.token}}
		for k, v := range params {
			form.Set(k, v)
		}

		req, err := http.NewRequest("POST", c.baseURL+"/"+method, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "Mozilla/5.0")
		if c.cookies != "" {
			req.Header.Set("Cookie", c.cookies)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("slack %s: %w", method, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("slack %s: read body: %w", method, err)
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("slack %s: HTTP %d", method, resp.StatusCode)
		}

		var result struct {
			OK    bool            `json:"ok"`
			Error string          `json:"error"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("slack %s: invalid JSON: %w", method, err)
		}

		if result.OK {
			return body, nil
		}

		if authErrors[result.Error] {
			return nil, &AuthError{Code: result.Error}
		}

		if result.Error == "ratelimited" {
			retryAfter := 30
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if n, err := strconv.Atoi(ra); err == nil {
					retryAfter = n
				}
			}
			fmt.Printf("Rate limited — waiting %ds…\n", retryAfter)
			time.Sleep(time.Duration(retryAfter) * time.Second)
			continue
		}

		return nil, fmt.Errorf("slack %s: %s", method, result.Error)
	}
	return nil, fmt.Errorf("slack %s: exceeded retry limit", method)
}

// IsAuthError returns true if the error is a Slack authentication error.
func IsAuthError(err error) bool {
	var ae *AuthError
	return errors.As(err, &ae)
}

// ConversationsInfo returns channel metadata.
func (c *Client) ConversationsInfo(channel string) (json.RawMessage, error) {
	return c.post("conversations.info", map[string]string{"channel": channel})
}

// ConversationsList returns a page of channels.
func (c *Client) ConversationsList(params map[string]string) (json.RawMessage, error) {
	return c.post("conversations.list", params)
}

// ConversationsHistory returns a page of messages.
func (c *Client) ConversationsHistory(params map[string]string) (json.RawMessage, error) {
	return c.post("conversations.history", params)
}

// ConversationsReplies returns thread replies.
func (c *Client) ConversationsReplies(params map[string]string) (json.RawMessage, error) {
	return c.post("conversations.replies", params)
}

// UsersInfo returns a user profile.
func (c *Client) UsersInfo(userID string) (json.RawMessage, error) {
	return c.post("users.info", map[string]string{"user": userID})
}

// SearchMessages performs a Slack search.
func (c *Client) SearchMessages(query string, count, page int) (json.RawMessage, error) {
	return c.post("search.messages", map[string]string{
		"query": query,
		"count": strconv.Itoa(count),
		"page":  strconv.Itoa(page),
	})
}
