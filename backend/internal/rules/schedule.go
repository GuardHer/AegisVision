package rules

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	// Embed IANA timezone database so time.LoadLocation works on Windows and minimal containers without CGO.
	_ "time/tzdata"
)

// WeeklyWindow defines a recurring weekly armed interval in HH:MM format.
// Days uses ISO weekday numbering (1=Monday .. 7=Sunday).
// When End < Start (e.g., "20:00" to "08:00"), the window spans overnight into the next calendar day.
type WeeklyWindow struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// IncludeDateWindow defines a one-off armed date exception (e.g., weekend overtime).
// Omitting Start and End arms the entire calendar day (00:00-24:00).
type IncludeDateWindow struct {
	Date  string `json:"date"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// ArmingSchedule evaluates whether a rule is armed at a given timestamp.
type ArmingSchedule struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Timezone      string              `json:"timezone"`
	WeeklyWindows []WeeklyWindow      `json:"weekly_windows"`
	ExcludeDates  []string            `json:"exclude_dates"`
	IncludeDates  []IncludeDateWindow `json:"include_dates"`
	Enabled       bool                `json:"enabled"`

	loc        *time.Location
	excludeSet map[string]struct{}
}

// ParseArmingSchedule builds a compiled ArmingSchedule from database JSONB columns.
func ParseArmingSchedule(id, name, tz string, enabled bool, weeklyJSON, excludeJSON, includeJSON []byte) (*ArmingSchedule, error) {
	tzName := strings.TrimSpace(tz)
	if tzName == "" {
		tzName = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule timezone %q: %w", tzName, err)
	}

	var weekly []WeeklyWindow
	if len(weeklyJSON) > 0 {
		if err := json.Unmarshal(weeklyJSON, &weekly); err != nil {
			return nil, fmt.Errorf("parse weekly_windows: %w", err)
		}
	}
	var exclude []string
	if len(excludeJSON) > 0 {
		if err := json.Unmarshal(excludeJSON, &exclude); err != nil {
			return nil, fmt.Errorf("parse exclude_dates: %w", err)
		}
	}
	var include []IncludeDateWindow
	if len(includeJSON) > 0 {
		if err := json.Unmarshal(includeJSON, &include); err != nil {
			return nil, fmt.Errorf("parse include_dates: %w", err)
		}
	}

	excludeSet := make(map[string]struct{}, len(exclude))
	for _, d := range exclude {
		trimmed := strings.TrimSpace(d)
		if trimmed != "" {
			excludeSet[trimmed] = struct{}{}
		}
	}

	return &ArmingSchedule{
		ID:            id,
		Name:          name,
		Timezone:      tzName,
		WeeklyWindows: weekly,
		ExcludeDates:  exclude,
		IncludeDates:  include,
		Enabled:       enabled,
		loc:           loc,
		excludeSet:    excludeSet,
	}, nil
}

// IsArmed returns true when ts falls within an armed window under precedence:
// disabled -> false; exclude_dates > include_dates > weekly_windows.
func (s *ArmingSchedule) IsArmed(ts time.Time) bool {
	if s == nil {
		return true
	}
	if !s.Enabled {
		return false
	}

	loc := s.loc
	if loc == nil {
		var err error
		loc, err = time.LoadLocation(s.Timezone)
		if err != nil {
			loc = time.UTC
		}
	}

	local := ts.In(loc)
	dateKey := local.Format("2006-01-02")
	minuteOfDay := local.Hour()*60 + local.Minute()

	// Priority 1: exclude_dates immediately disarm the entire calendar day in template timezone
	// (including post-midnight tails of overnight shifts from the previous evening).
	if _, excluded := s.excludeSet[dateKey]; excluded {
		return false
	}

	// Priority 2: include_dates (overtime / special production days).
	for _, inc := range s.IncludeDates {
		if strings.TrimSpace(inc.Date) != dateKey {
			continue
		}
		startMin := 0
		endMin := 24 * 60
		if strings.TrimSpace(inc.Start) != "" {
			m, ok := parseClockMinutes(inc.Start, false)
			if !ok {
				continue
			}
			startMin = m
		}
		if strings.TrimSpace(inc.End) != "" {
			m, ok := parseClockMinutes(inc.End, true)
			if !ok {
				continue
			}
			endMin = m
		}
		if minuteOfDay >= startMin && minuteOfDay < endMin {
			return true
		}
	}

	// Priority 3: recurring weekly_windows (including overnight shifts where end < start).
	todayWD := isoWeekday(local.Weekday())
	prevWD := isoWeekday(local.AddDate(0, 0, -1).Weekday())
	for _, w := range s.WeeklyWindows {
		startMin, okStart := parseClockMinutes(w.Start, false)
		endMin, okEnd := parseClockMinutes(w.End, true)
		if !okStart || !okEnd || startMin == endMin {
			continue
		}
		if startMin < endMin {
			if containsInt(w.Days, todayWD) && minuteOfDay >= startMin && minuteOfDay < endMin {
				return true
			}
		} else {
			// Overnight window: [startMin, 24:00) on day D and [00:00, endMin) on day D+1.
			if containsInt(w.Days, todayWD) && minuteOfDay >= startMin {
				return true
			}
			if containsInt(w.Days, prevWD) && minuteOfDay < endMin {
				return true
			}
		}
	}

	return false
}

func isoWeekday(wd time.Weekday) int {
	if wd == time.Sunday {
		return 7
	}
	return int(wd)
}

func containsInt(slice []int, target int) bool {
	for _, v := range slice {
		if v == target {
			return true
		}
	}
	return false
}

func parseClockMinutes(raw string, allow24 bool) (int, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || m < 0 || m > 59 {
		return 0, false
	}
	if allow24 && h == 24 && m == 0 {
		return 24 * 60, true
	}
	if h < 0 || h > 23 {
		return 0, false
	}
	return h*60 + m, true
}
