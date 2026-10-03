package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A minimal Messages API client: tool use, prompt caching on the tool list, usage.

type apiTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   []block         `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Source    *imageSource    `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// message content is kept as raw JSON so assistant turns go back verbatim: thinking
// blocks carry a signature the API (or a router) verifies on the next turn.
type message struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

func raws(bs []block) []json.RawMessage {
	out := make([]json.RawMessage, len(bs))
	for i, b := range bs {
		out[i], _ = json.Marshal(b)
	}
	return out
}

func parseBlocks(rs []json.RawMessage) []block {
	out := make([]block, 0, len(rs))
	for _, r := range rs {
		var b block
		if json.Unmarshal(r, &b) == nil {
			out = append(out, b)
		}
	}
	return out
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (u *usage) add(o usage) {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreationInputTokens += o.CacheCreationInputTokens
	u.CacheReadInputTokens += o.CacheReadInputTokens
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Tools     []apiTool `json:"tools,omitempty"`
	Messages  []message `json:"messages"`
}

type response struct {
	Model      string            `json:"model"` // the model that actually served it (a router may substitute)
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      usage             `json:"usage"`
}

type client struct {
	key     string // x-api-key (ANTHROPIC_API_KEY)
	token   string // Authorization: Bearer (ANTHROPIC_AUTH_TOKEN, e.g. a local proxy)
	baseURL string // ANTHROPIC_BASE_URL; "/v1/messages" is appended
	http    *http.Client
}

func (c *client) create(ctx context.Context, req request) (*response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var last error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
		hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if c.token != "" {
			hr.Header.Set("Authorization", "Bearer "+c.token)
		} else {
			hr.Header.Set("x-api-key", c.key)
		}
		hr.Header.Set("anthropic-version", "2023-06-01")
		hr.Header.Set("content-type", "application/json")
		resp, err := c.http.Do(hr)
		if err != nil {
			last = err
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// A router that lost the target a signed thinking turn came from: transient.
		routerLost := resp.StatusCode == 400 && strings.Contains(string(data), "no retained native target")
		if routerLost {
			// The router moved the conversation to another target: resend the history without
			// the thinking blocks the old target signed.
			req.Messages = withoutThinking(req.Messages)
			if body, err = json.Marshal(req); err != nil {
				return nil, err
			}
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 || routerLost {
			last = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 300))
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 500))
		}
		var out response
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	return nil, fmt.Errorf("messages API: %w", last)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// withoutThinking drops thinking / redacted_thinking blocks from assistant turns.
func withoutThinking(msgs []message) []message {
	out := make([]message, len(msgs))
	for i, m := range msgs {
		out[i] = message{Role: m.Role}
		for _, raw := range m.Content {
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &probe) == nil && (probe.Type == "thinking" || probe.Type == "redacted_thinking") {
				continue
			}
			out[i].Content = append(out[i].Content, raw)
		}
	}
	return out
}
