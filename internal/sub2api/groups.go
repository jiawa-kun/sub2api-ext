package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Group is a Sub2API account group.
type Group struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
	Count    int    `json:"account_count,omitempty"`
}

// ListGroups loads admin groups. Empty platform merges common platforms.
func (c *Client) ListGroups(ctx context.Context, platform string) ([]Group, error) {
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential empty")
	}

	platforms := []string{}
	if p := strings.TrimSpace(platform); p != "" {
		platforms = []string{p}
	} else {
		platforms = []string{"", "openai", "anthropic", "claude", "gemini", "google", "grok", "xai", "deepseek", "openrouter"}
	}

	var all []Group
	var lastErr error

	for _, p := range platforms {
		// 1) all endpoint
		allURL := c.baseURL + "/api/v1/admin/groups/all"
		if p != "" {
			allURL += "?platform=" + url.QueryEscape(p)
		}
		if groups, err := c.fetchGroupsURL(ctx, allURL); err != nil {
			lastErr = err
		} else {
			if p != "" {
				for i := range groups {
					if groups[i].Platform == "" {
						groups[i].Platform = p
					}
				}
			}
			all = append(all, groups...)
			// if unfiltered all returned data, still continue platforms to catch scoped-only groups
		}

		// 2) paged endpoint first page (and more if needed)
		page := 1
		for page <= 5 {
			q := url.Values{}
			q.Set("page", strconv.Itoa(page))
			q.Set("page_size", "100")
			if p != "" {
				q.Set("platform", p)
			}
			pageURL := c.baseURL + "/api/v1/admin/groups?" + q.Encode()
			groups, err := c.fetchGroupsURL(ctx, pageURL)
			if err != nil {
				lastErr = err
				break
			}
			if p != "" {
				for i := range groups {
					if groups[i].Platform == "" {
						groups[i].Platform = p
					}
				}
			}
			all = append(all, groups...)
			if len(groups) < 100 {
				break
			}
			page++
		}
	}

	out := dedupeGroups(all)
	if len(out) == 0 {
		if accGroups, err := c.listGroupsFromAccounts(ctx); err == nil {
			out = dedupeGroups(accGroups)
		} else if lastErr != nil {
			return nil, lastErr
		}
	}
	return out, nil
}

func (c *Client) fetchGroupsURL(ctx context.Context, endpoint string) ([]Group, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
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
		return nil, fmt.Errorf("list groups failed: status=%d body=%s", status, truncate(body, 200))
	}
	return parseGroupList(body), nil
}

func (c *Client) listGroupsFromAccounts(ctx context.Context) ([]Group, error) {
	set := map[string]Group{}
	for page := 1; page <= 8; page++ {
		res, err := c.ListAccountsPage(ctx, "", page, 100, "Asia/Shanghai")
		if err != nil {
			return nil, err
		}
		for _, a := range res.Items {
			// Account.Group may be empty; parse raw credentials-adjacent fields via name heuristics later
			g := strings.TrimSpace(a.EffectiveGroup())
			if g == "" {
				continue
			}
			if _, ok := set[g]; !ok {
				set[g] = Group{ID: g, Name: g}
			}
		}
		if page >= res.Pages || len(res.Items) == 0 {
			break
		}
	}
	out := make([]Group, 0, len(set))
	for _, g := range set {
		out = append(out, g)
	}
	return out, nil
}

// CollectGroupModels aggregates model_mapping keys from accounts in a group.
func (c *Client) CollectGroupModels(ctx context.Context, group, timezone string) ([]string, error) {
	return c.CollectGroupModelsLimited(ctx, group, timezone, 0)
}

// CollectGroupModelsLimited is like CollectGroupModels but stops after maxPages (0 = all).
func (c *Client) CollectGroupModelsLimited(ctx context.Context, group, timezone string, maxPages int) ([]string, error) {
	if maxPages < 0 {
		maxPages = 0
	}
	pageSize := 100
	set := map[string]struct{}{}
	page := 1
	for {
		res, err := c.ListAccountsPage(ctx, group, page, pageSize, timezone)
		if err != nil {
			return nil, err
		}
		for _, a := range res.Items {
			for _, m := range a.ModelMappingKeys() {
				set[m] = struct{}{}
			}
		}
		if page >= res.Pages || len(res.Items) == 0 {
			break
		}
		page++
		if maxPages > 0 && page > maxPages {
			break
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out, nil
}

func parseGroupList(body []byte) []Group {
	var env apiEnvelope
	raw := body
	if err := json.Unmarshal(body, &env); err == nil && len(env.Data) > 0 && string(env.Data) != "null" {
		raw = env.Data
	}

	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		return mapsToGroups(arr)
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	for _, key := range []string{"items", "groups", "list", "data", "results"} {
		if v, ok := obj[key]; ok {
			var nested []map[string]any
			if err := json.Unmarshal(v, &nested); err == nil {
				return mapsToGroups(nested)
			}
		}
	}
	return nil
}

func mapsToGroups(items []map[string]any) []Group {
	out := make([]Group, 0, len(items))
	for _, it := range items {
		id := firstString(it, "id", "group_id", "groupId", "value", "key", "code", "slug")
		name := firstString(it, "name", "group_name", "groupName", "label", "title", "display_name")
		platform := firstString(it, "platform", "provider")
		count := firstInt(it, "account_count", "accounts_count", "count", "total")
		if id == "" && name == "" {
			continue
		}
		if id == "" {
			id = name
		}
		if name == "" {
			name = id
		}
		out = append(out, Group{ID: id, Name: name, Platform: platform, Count: count})
	}
	return out
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				s := strings.TrimSpace(t)
				if s != "" {
					return s
				}
			case float64:
				return fmt.Sprintf("%.0f", t)
			case json.Number:
				return t.String()
			case int:
				return strconv.Itoa(t)
			case int64:
				return strconv.FormatInt(t, 10)
			}
		}
	}
	return ""
}

func firstInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return int(t)
			case json.Number:
				n, _ := t.Int64()
				return int(n)
			case int:
				return t
			case string:
				n, _ := strconv.Atoi(strings.TrimSpace(t))
				return n
			}
		}
	}
	return 0
}

func dedupeGroups(in []Group) []Group {
	seen := map[string]Group{}
	for _, g := range in {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = id
		}
		cur, ok := seen[id]
		if !ok {
			seen[id] = Group{ID: id, Name: name, Platform: g.Platform, Count: g.Count}
			continue
		}
		if len(name) > len(cur.Name) {
			cur.Name = name
		}
		if cur.Platform == "" && g.Platform != "" {
			cur.Platform = g.Platform
		}
		if g.Count > cur.Count {
			cur.Count = g.Count
		}
		seen[id] = cur
	}
	out := make([]Group, 0, len(seen))
	for _, g := range seen {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
