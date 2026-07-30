package patrol

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronExpr is a minimal 5-field cron (min hour dom month dow).
type CronExpr struct {
	raw    string
	minute field
	hour   field
	dom    field
	month  field
	dow    field
}

type field struct {
	any  bool
	vals map[int]struct{}
}

// ParseCron parses standard 5-field cron with lists/ranges/steps.
func ParseCron(expr string) (*CronExpr, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty cron")
	}
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("want 5 fields, got %d", len(parts))
	}
	minF, err := parseField(parts[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	hourF, err := parseField(parts[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	domF, err := parseField(parts[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("dom: %w", err)
	}
	monF, err := parseField(parts[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	dowF, err := parseField(parts[4], 0, 6) // 0=Sunday
	if err != nil {
		return nil, fmt.Errorf("dow: %w", err)
	}
	return &CronExpr{raw: expr, minute: minF, hour: hourF, dom: domF, month: monF, dow: dowF}, nil
}

func (c *CronExpr) String() string { return c.raw }

// Matches reports whether t matches this expression (second ignored).
func (c *CronExpr) Matches(t time.Time) bool {
	if c == nil {
		return false
	}
	if !c.minute.match(t.Minute()) {
		return false
	}
	if !c.hour.match(t.Hour()) {
		return false
	}
	if !c.month.match(int(t.Month())) {
		return false
	}
	// cron: if both dom and dow are restricted, either may match (classic cron OR semantics)
	domAny := c.dom.any
	dowAny := c.dow.any
	domOK := c.dom.match(t.Day())
	dowOK := c.dow.match(int(t.Weekday())) // time.Weekday Sunday=0
	if !domAny && !dowAny {
		return domOK || dowOK
	}
	if !domAny && !domOK {
		return false
	}
	if !dowAny && !dowOK {
		return false
	}
	return true
}

// Next returns the next matching minute strictly after t.
// Scans at most 8 days; returns zero time if none found.
func (c *CronExpr) Next(after time.Time) time.Time {
	if c == nil {
		return time.Time{}
	}
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(8 * 24 * time.Hour)
	for !t.After(limit) {
		if c.Matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (f field) match(v int) bool {
	if f.any {
		return true
	}
	_, ok := f.vals[v]
	return ok
}

func parseField(raw string, min, max int) (field, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return field{}, fmt.Errorf("empty")
	}
	if raw == "*" {
		return field{any: true}, nil
	}
	out := field{vals: map[int]struct{}{}}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step := 1
		base := part
		if i := strings.Index(part, "/"); i >= 0 {
			base = part[:i]
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return field{}, fmt.Errorf("bad step in %q", part)
			}
			step = n
		}
		var start, end int
		if base == "*" {
			start, end = min, max
		} else if j := strings.Index(base, "-"); j >= 0 {
			a, err1 := strconv.Atoi(base[:j])
			b, err2 := strconv.Atoi(base[j+1:])
			if err1 != nil || err2 != nil {
				return field{}, fmt.Errorf("bad range %q", base)
			}
			start, end = a, b
		} else {
			n, err := strconv.Atoi(base)
			if err != nil {
				return field{}, fmt.Errorf("bad value %q", base)
			}
			start, end = n, n
		}
		if start > end {
			return field{}, fmt.Errorf("range start>end in %q", part)
		}
		if start < min || end > max {
			return field{}, fmt.Errorf("out of range %d-%d for %d..%d", start, end, min, max)
		}
		for v := start; v <= end; v += step {
			out.vals[v] = struct{}{}
		}
	}
	if len(out.vals) == 0 {
		return field{}, fmt.Errorf("no values in %q", raw)
	}
	return out, nil
}
