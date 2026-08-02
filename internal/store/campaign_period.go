package store

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	CampaignTimezone         = "Asia/Shanghai"
	CampaignFrequencyOnce    = "once"
	CampaignFrequencyDaily   = "daily"
	CampaignFrequencyWeekly  = "weekly"
	CampaignFrequencyMonthly = "monthly"
)

// CampaignPeriod is one complete settlement window in the campaign timezone.
type CampaignPeriod struct {
	Key       string
	StartDate string
	EndDate   string
}

var campaignDailyKey = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var campaignWeeklyKey = regexp.MustCompile(`^(\d{4})-W(\d{2})$`)
var campaignMonthlyKey = regexp.MustCompile(`^\d{4}-\d{2}$`)

func NormalizeCampaignFrequency(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CampaignFrequencyDaily, CampaignFrequencyWeekly, CampaignFrequencyMonthly:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return CampaignFrequencyOnce
	}
}

func NormalizeCampaignSettlementTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "03:00"
	}
	if _, err := time.Parse("15:04", value); err != nil {
		return "03:00"
	}
	return value
}

func ValidateCampaignSettlementTime(value string) error {
	value = strings.TrimSpace(value)
	if _, err := time.Parse("15:04", value); err != nil {
		return fmt.Errorf("settlement_time must use HH:MM")
	}
	return nil
}

// PreviousCampaignPeriod returns the last fully completed natural period.
func PreviousCampaignPeriod(frequency string, now time.Time, loc *time.Location) (CampaignPeriod, error) {
	frequency = NormalizeCampaignFrequency(frequency)
	if loc == nil {
		loc, _ = time.LoadLocation(CampaignTimezone)
	}
	local := now.In(loc)
	date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch frequency {
	case CampaignFrequencyDaily:
		day := date.AddDate(0, 0, -1)
		key := day.Format("2006-01-02")
		return CampaignPeriod{Key: key, StartDate: key, EndDate: key}, nil
	case CampaignFrequencyWeekly:
		monday := date.AddDate(0, 0, -daysSinceMonday(date))
		start := monday.AddDate(0, 0, -7)
		end := start.AddDate(0, 0, 6)
		year, week := start.ISOWeek()
		return CampaignPeriod{
			Key:       fmt.Sprintf("%04d-W%02d", year, week),
			StartDate: start.Format("2006-01-02"),
			EndDate:   end.Format("2006-01-02"),
		}, nil
	case CampaignFrequencyMonthly:
		currentMonth := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, loc)
		start := currentMonth.AddDate(0, -1, 0)
		end := currentMonth.AddDate(0, 0, -1)
		return CampaignPeriod{Key: start.Format("2006-01"), StartDate: start.Format("2006-01-02"), EndDate: end.Format("2006-01-02")}, nil
	default:
		return CampaignPeriod{}, fmt.Errorf("frequency %q has no automatic period", frequency)
	}
}

// CampaignPeriodFromKey parses a period key for the selected frequency.
func CampaignPeriodFromKey(frequency, key string, loc *time.Location) (CampaignPeriod, error) {
	frequency = NormalizeCampaignFrequency(frequency)
	key = strings.TrimSpace(key)
	if loc == nil {
		loc, _ = time.LoadLocation(CampaignTimezone)
	}
	switch frequency {
	case CampaignFrequencyDaily:
		if !campaignDailyKey.MatchString(key) {
			return CampaignPeriod{}, fmt.Errorf("daily period key must be YYYY-MM-DD")
		}
		if day, err := time.ParseInLocation("2006-01-02", key, loc); err == nil && day.Format("2006-01-02") == key {
			return CampaignPeriod{Key: key, StartDate: key, EndDate: key}, nil
		}
		return CampaignPeriod{}, fmt.Errorf("invalid daily period key")
	case CampaignFrequencyWeekly:
		m := campaignWeeklyKey.FindStringSubmatch(key)
		if len(m) != 3 {
			return CampaignPeriod{}, fmt.Errorf("weekly period key must be YYYY-Www")
		}
		year, _ := strconv.Atoi(m[1])
		week, _ := strconv.Atoi(m[2])
		start, ok := isoWeekStart(year, week, loc)
		if !ok {
			return CampaignPeriod{}, fmt.Errorf("invalid weekly period key")
		}
		end := start.AddDate(0, 0, 6)
		return CampaignPeriod{Key: key, StartDate: start.Format("2006-01-02"), EndDate: end.Format("2006-01-02")}, nil
	case CampaignFrequencyMonthly:
		if !campaignMonthlyKey.MatchString(key) {
			return CampaignPeriod{}, fmt.Errorf("monthly period key must be YYYY-MM")
		}
		start, err := time.ParseInLocation("2006-01", key, loc)
		if err != nil || start.Format("2006-01") != key {
			return CampaignPeriod{}, fmt.Errorf("invalid monthly period key")
		}
		start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, loc)
		end := start.AddDate(0, 1, -1)
		return CampaignPeriod{Key: key, StartDate: start.Format("2006-01-02"), EndDate: end.Format("2006-01-02")}, nil
	default:
		if key == "" || key == CampaignFrequencyOnce {
			return CampaignPeriod{Key: CampaignFrequencyOnce}, nil
		}
		return CampaignPeriod{}, fmt.Errorf("once campaign period key must be once")
	}
}

func PeriodInsideCampaign(period CampaignPeriod, startDate, endDate string) bool {
	return period.StartDate != "" && period.EndDate != "" && startDate != "" && endDate != "" && period.StartDate >= startDate && period.EndDate <= endDate
}

func daysSinceMonday(t time.Time) int {
	day := int(t.Weekday())
	if day == 0 {
		return 6
	}
	return day - 1
}

func isoWeekStart(year, week int, loc *time.Location) (time.Time, bool) {
	if week < 1 || week > 53 || year < 1 {
		return time.Time{}, false
	}
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, loc)
	start := jan4.AddDate(0, 0, -daysSinceMonday(jan4)).AddDate(0, 0, (week-1)*7)
	y, w := start.ISOWeek()
	if y != year || w != week {
		return time.Time{}, false
	}
	return start, true
}
