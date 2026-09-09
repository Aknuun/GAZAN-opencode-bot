package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type OCClient struct {
	base   string
	agent  string
	client *http.Client
}

func newOCClient(base, agent string) *OCClient {
	return &OCClient{
		base:  strings.TrimRight(base, "/"),
		agent: agent,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

type OCSession struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	Title     string  `json:"title"`
	Directory string  `json:"directory"`
	Cost      float64 `json:"cost"`
	Tokens    struct {
		Input  int `json:"input"`
		Output int `json:"output"`
	} `json:"tokens"`
	Summary struct {
		Files int `json:"files"`
	} `json:"summary"`
	ModelID string `json:"modelID"`
	Time    struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

type OCPart struct {
	Type  string       `json:"type"`
	Text  string       `json:"text"`
	Tool  string       `json:"tool"`
	State *OCPartState `json:"state"`
}

type OCPartState struct {
	Status string         `json:"status"`
	Title  string         `json:"title"`
	Input  map[string]any `json:"input"`
}

type OCMessage struct {
	Info struct {
		ID        string `json:"id"`
		Role      string `json:"role"`
		SessionID string `json:"sessionID"`
		ModelID   string `json:"modelID"`
		Finish    string `json:"finish"`
	} `json:"info"`
	Parts []OCPart `json:"parts"`
}

func (c *OCClient) doJSON(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		return fmt.Errorf("opencode %s %s: %s %s", method, path, resp.Status, string(b))
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *OCClient) CreateSession(ctx context.Context) (*OCSession, error) {
	var s OCSession
	err := c.doJSON(ctx, http.MethodPost, "/session", map[string]any{}, &s)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *OCClient) ListSessions(ctx context.Context, limit int) ([]OCSession, error) {
	if limit <= 0 {
		limit = 100
	}
	var list []OCSession
	// limit سمت سرور اعمال می‌شود؛ سرور به‌ترتیب «آخرین فعالیت» برمی‌گرداند
	if err := c.doJSON(ctx, http.MethodGet, "/session?limit="+strconv.Itoa(limit), nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// DeleteSession نشست و تمام داده‌هایش را روی سرور حذف می‌کند
func (c *OCClient) DeleteSession(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/session/"+url.PathEscape(id), nil, nil)
	if err != nil && strings.Contains(err.Error(), "404") {
		// از قبل حذف شده؛ اشکالی ندارد
		return nil
	}
	return err
}

func (c *OCClient) GetSession(ctx context.Context, id string) (*OCSession, error) {
	var s OCSession
	if err := c.doJSON(ctx, http.MethodGet, "/session/"+url.PathEscape(id), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (c *OCClient) PromptAsync(ctx context.Context, sessionID, prompt, agent string) error {
	if agent == "" {
		agent = c.agent
	}
	body := map[string]any{
		"agent": agent,
		"parts": []map[string]any{{"type": "text", "text": prompt}},
	}
	return c.doJSON(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/prompt_async", body, nil)
}

func (c *OCClient) LastMessage(ctx context.Context, sessionID string) (*OCMessage, error) {
	var list []OCMessage
	err := c.doJSON(ctx, http.MethodGet,
		"/session/"+url.PathEscape(sessionID)+"/message?limit=1", nil, &list)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// ListMessages فهرست پیام‌های نشست (قدیمی→جدید)؛ limit تعداد پیام‌های آخر
func (c *OCClient) ListMessages(ctx context.Context, sessionID string, limit int) ([]OCMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	var list []OCMessage
	err := c.doJSON(ctx, http.MethodGet,
		"/session/"+url.PathEscape(sessionID)+"/message?limit="+strconv.Itoa(limit), nil, &list)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (c *OCClient) Abort(ctx context.Context, sessionID string) error {
	return c.doJSON(ctx, http.MethodPost,
		"/session/"+url.PathEscape(sessionID)+"/abort", nil, nil)
}

// ---------- سؤال‌های تعاملی (مثل CLI خود opencode) ----------

type QOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type QInfo struct {
	Question string    `json:"question"`
	Header   string    `json:"header"`
	Options  []QOption `json:"options"`
	Multiple bool      `json:"multiple,omitempty"`
	Custom   *bool     `json:"custom,omitempty"` // nil یعنی true (پاسخ آزاد مجاز)
}

type QRequest struct {
	ID        string  `json:"id"`
	SessionID string  `json:"sessionID"`
	Questions []QInfo `json:"questions"`
	Tool      *struct {
		MessageID string `json:"messageID"`
		CallID    string `json:"callID"`
	} `json:"tool,omitempty"`
}

func (c *OCClient) ListQuestions(ctx context.Context) ([]QRequest, error) {
	var list []QRequest
	if err := c.doJSON(ctx, http.MethodGet, "/question", nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (c *OCClient) ReplyQuestion(ctx context.Context, requestID string, answers [][]string) error {
	body := map[string]any{"answers": answers}
	return c.doJSON(ctx, http.MethodPost,
		"/question/"+url.PathEscape(requestID)+"/reply", body, nil)
}

func (c *OCClient) RejectQuestion(ctx context.Context, requestID string) error {
	return c.doJSON(ctx, http.MethodPost,
		"/question/"+url.PathEscape(requestID)+"/reject", nil, nil)
}
