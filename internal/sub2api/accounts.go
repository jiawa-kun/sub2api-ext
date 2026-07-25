package sub2api

import (
	"bufio"
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

// Account is a Sub2API upstream account used by account-patrol.
type Account struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Schedulable bool            `json:"schedulable"`
	Platform    string          `json:"platform"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Group       string          `json:"group"`
	Credentials json.RawMessage `json:"credentials"`
}

// AccountListResult is one page of admin accounts.
type AccountListResult struct {
	Items []Account `json:"items"`
	Page  int       `json:"page"`
	Pages int       `json:"pages"`
	Total int       `json:"total"`
}

// TestModelResult is the parsed SSE test outcome.
type TestModelResult struct {
	OK     bool
	Reason string
}

type accountListData struct {
	Items []Account `json:"items"`
	Page  int       `json:"page"`
	Pages int       `json:"pages"`
	Total int       `json:"total"`
}

// ListAccountsPage lists one page of accounts optionally filtered by group.
func (c *Client) ListAccountsPage(ctx context.Context, group string, page, pageSize int, timezone string) (*AccountListResult, error) {
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential empty")
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 100
	}
	if timezone == "" {
		timezone = "Asia/Shanghai"
	}

	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("page_size", strconv.Itoa(pageSize))
	q.Set("platform", "")
	q.Set("type", "")
	q.Set("status", "")
	q.Set("privacy_mode", "")
	q.Set("group", strings.TrimSpace(group))
	q.Set("search", "")
	q.Set("timezone", timezone)

	reqURL := fmt.Sprintf("%s/api/v1/admin/accounts?%s", c.baseURL, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	c.applyAdminAuth(req)
	req.Header.Set("Accept", "application/json")
	c.applyInternalHost(req)

	body, status, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, fmt.Errorf("list accounts failed: status=%d body=%s", status, truncate(body, 300))
	}
	if err := ensureAPICodeOK(body); err != nil {
		return nil, err
	}

	var data accountListData
	if err := decodeData(body, &data); err != nil {
		return nil, fmt.Errorf("decode accounts: %w", err)
	}
	if data.Pages <= 0 {
		data.Pages = 1
	}
	return &AccountListResult{
		Items: data.Items,
		Page:  data.Page,
		Pages: data.Pages,
		Total: data.Total,
	}, nil
}

// ListAllAccounts walks pages until exhausted.
func (c *Client) ListAllAccounts(ctx context.Context, group string, pageSize int, timezone string) ([]Account, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	var all []Account
	page := 1
	for {
		res, err := c.ListAccountsPage(ctx, group, page, pageSize, timezone)
		if err != nil {
			return nil, err
		}
		all = append(all, res.Items...)
		if page >= res.Pages || len(res.Items) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// SetAccountSchedulable toggles account schedulable flag.
func (c *Client) SetAccountSchedulable(ctx context.Context, accountID int64, schedulable bool) error {
	if c.AdminToken() == "" {
		return fmt.Errorf("admin credential empty")
	}
	payload, _ := json.Marshal(map[string]any{"schedulable": schedulable})
	reqURL := fmt.Sprintf("%s/api/v1/admin/accounts/%d/schedulable", c.baseURL, accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	c.applyAdminAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.applyInternalHost(req)

	body, status, err := c.do(req)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("set schedulable failed: status=%d body=%s", status, truncate(body, 300))
	}
	return ensureAPICodeOK(body)
}

// DeleteAccount deletes an upstream account.
func (c *Client) DeleteAccount(ctx context.Context, accountID int64) error {
	if c.AdminToken() == "" {
		return fmt.Errorf("admin credential empty")
	}
	reqURL := fmt.Sprintf("%s/api/v1/admin/accounts/%d", c.baseURL, accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return err
	}
	c.applyAdminAuth(req)
	req.Header.Set("Accept", "application/json")
	c.applyInternalHost(req)

	body, status, err := c.do(req)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("delete account failed: status=%d body=%s", status, truncate(body, 300))
	}
	return ensureAPICodeOK(body)
}

// TestAccountModel posts admin account model test and waits for SSE test_complete.
// timeout is idle timeout between stream chunks (reset on each read).
func (c *Client) TestAccountModel(ctx context.Context, accountID int64, modelID, prompt string, timeout time.Duration) TestModelResult {
	if c.AdminToken() == "" {
		return TestModelResult{OK: false, Reason: "admin credential empty"}
	}
	if strings.TrimSpace(modelID) == "" {
		return TestModelResult{OK: false, Reason: "empty model_id"}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "hi"
	}
	if timeout < time.Second {
		timeout = 45 * time.Second
	}

	payload, _ := json.Marshal(map[string]any{
		"model_id": modelID,
		"prompt":   prompt,
	})
	reqURL := fmt.Sprintf("%s/api/v1/admin/accounts/%d/test", c.baseURL, accountID)

	// Stream can outlive normal API timeout; bound by parent ctx + idle timeout.
	streamClient := &http.Client{Timeout: 0}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	timedOut := make(chan struct{}, 1)
	var idleTimer *time.Timer
	resetIdle := func() {
		if idleTimer == nil {
			idleTimer = time.AfterFunc(timeout, func() {
				select {
				case timedOut <- struct{}{}:
				default:
				}
				cancel()
			})
			return
		}
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(timeout)
	}
	resetIdle()
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return TestModelResult{OK: false, Reason: err.Error()}
	}
	c.applyAdminAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	c.applyInternalHost(req)

	resp, err := streamClient.Do(req)
	if err != nil {
		select {
		case <-timedOut:
			return TestModelResult{OK: false, Reason: fmt.Sprintf("模型 %s 流式超时", modelID)}
		default:
		}
		if reqCtx.Err() != nil && ctx.Err() == nil {
			return TestModelResult{OK: false, Reason: fmt.Sprintf("模型 %s 流式超时", modelID)}
		}
		return TestModelResult{OK: false, Reason: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return TestModelResult{OK: false, Reason: fmt.Sprintf("HTTP %d %s", resp.StatusCode, truncate(b, 200))}
	}

	reader := bufio.NewReader(resp.Body)
	var eventBuf strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			resetIdle()
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if res, done := parseSSEEvent(eventBuf.String()); done {
					return res
				}
				eventBuf.Reset()
			} else {
				eventBuf.WriteString(line)
				eventBuf.WriteByte('\n')
			}
		}
		if err != nil {
			if eventBuf.Len() > 0 {
				if res, done := parseSSEEvent(eventBuf.String()); done {
					return res
				}
			}
			select {
			case <-timedOut:
				return TestModelResult{OK: false, Reason: fmt.Sprintf("模型 %s 流式超时", modelID)}
			default:
			}
			if err == io.EOF {
				return TestModelResult{OK: false, Reason: "响应流结束但没有 test_complete"}
			}
			if reqCtx.Err() != nil {
				return TestModelResult{OK: false, Reason: fmt.Sprintf("模型 %s 流式超时", modelID)}
			}
			return TestModelResult{OK: false, Reason: err.Error()}
		}
	}
}


func parseSSEEvent(raw string) (TestModelResult, bool) {
	var dataLines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	for _, line := range dataLines {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		typ, _ := event["type"].(string)
		switch typ {
		case "error":
			reason := "未知错误"
			if v, ok := event["error"].(string); ok && v != "" {
				reason = v
			}
			return TestModelResult{OK: false, Reason: reason}, true
		case "test_complete":
			ok := false
			switch v := event["success"].(type) {
			case bool:
				ok = v
			case string:
				ok = strings.EqualFold(v, "true") || v == "1"
			case float64:
				ok = v != 0
			}
			if ok {
				return TestModelResult{OK: true, Reason: "success"}, true
			}
			return TestModelResult{OK: false, Reason: "test_complete=false"}, true
		}
	}
	return TestModelResult{}, false
}

// ModelMappingKeys extracts model ids from account credentials.model_mapping.
func (a Account) ModelMappingKeys() []string {
	if len(a.Credentials) == 0 {
		return nil
	}
	var cred map[string]json.RawMessage
	if err := json.Unmarshal(a.Credentials, &cred); err != nil {
		return nil
	}
	raw, ok := cred["model_mapping"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var mapping map[string]any
	if err := json.Unmarshal(raw, &mapping); err != nil {
		return nil
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		k = strings.TrimSpace(k)
		if k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

func ensureAPICodeOK(body []byte) error {
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		// some endpoints may return bare data
		return nil
	}
	if env.Code == nil {
		return nil
	}
	switch v := env.Code.(type) {
	case float64:
		if v == 0 {
			return nil
		}
		msg := env.Message
		if msg == "" {
			msg = env.Detail
		}
		if msg == "" {
			msg = fmt.Sprintf("code=%v", v)
		}
		return fmt.Errorf("%s", msg)
	case string:
		if v == "" || v == "0" || strings.EqualFold(v, "ok") || strings.EqualFold(v, "success") {
			return nil
		}
		msg := env.Message
		if msg == "" {
			msg = env.Detail
		}
		if msg == "" {
			msg = "code=" + v
		}
		return fmt.Errorf("%s", msg)
	default:
		return nil
	}
}
