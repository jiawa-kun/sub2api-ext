package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Group is a Sub2API account group.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListGroups loads admin groups. platform may be empty.
func (c *Client) ListGroups(ctx context.Context, platform string) ([]Group, error) {
	if c.AdminToken() == "" {
		return nil, fmt.Errorf("admin credential empty")
	}
	// Prefer "all" endpoint used by admin UI / userscript, then paged fallback.
	endpoints := []string{}
	if strings.TrimSpace(platform) != "" {
		endpoints = append(endpoints, fmt.Sprintf("%s/api/v1/admin/groups/all?platform=%s", c.baseURL, url.QueryEscape(platform)))
	}
	endpoints = append(endpoints,
		c.baseURL+"/api/v1/admin/groups/all",
		c.baseURL+"/api/v1/admin/groups?page=1&page_size=200",
	)

	var lastErr error
	for _, endpoint := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		c.applyAdminAuth(req)
		req.Header.Set("Accept", "application/json")
		c.applyInternalHost(req)
		body, status, err := c.do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if status >= 300 {
			lastErr = fmt.Errorf("list groups failed: status=%d body=%s", status, truncate(body, 200))
			continue
		}
		groups := parseGroupList(body)
		if len(groups) > 0 {
			return dedupeGroups(groups), nil
		}
		// empty but valid response still counts as success
		if err := ensureAPICodeOK(body); err == nil {
			return []Group{}, nil
		}
		lastErr = fmt.Errorf("decode groups failed: %s", truncate(body, 200))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no groups endpoint available")
	}
	return nil, lastErr
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
	// Try envelope.data first
	var env apiEnvelope
	raw := body
	if err := json.Unmarshal(body, &env); err == nil && len(env.Data) > 0 && string(env.Data) != "null" {
		raw = env.Data
	}

	// array directly
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		return mapsToGroups(arr)
	}

	// object with items/groups
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	for _, key := range []string{"items", "groups", "list", "data"} {
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
		id := firstString(it, "id", "group_id", "groupId", "value")
		name := firstString(it, "name", "group_name", "groupName", "label", "title")
		if id == "" && name == "" {
			continue
		}
		if id == "" {
			id = name
		}
		if name == "" {
			name = id
		}
		out = append(out, Group{ID: id, Name: name})
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
			}
		}
	}
	return ""
}

func dedupeGroups(in []Group) []Group {
	seen := map[string]struct{}{}
	out := make([]Group, 0, len(in))
	for _, g := range in {
		id := strings.TrimSpace(g.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = id
		}
		out = append(out, Group{ID: id, Name: name})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}
