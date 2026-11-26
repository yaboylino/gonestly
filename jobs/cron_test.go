package jobs

import (
	"strings"
	"testing"
	"time"
)

func TestScheduleNext(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip("no time zone data")
	}
	at := func(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, loc)
	}
	for name, tt := range map[string]struct {
		spec string
		loc  *time.Location
		from time.Time
		want time.Time
	}{
		"every minute":             {"* * * * *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 1, 5, 12, 1)},
		"seconds are ignored":      {"* * * * *", time.UTC, time.Date(2026, 1, 5, 12, 0, 59, 0, time.UTC), at(time.UTC, 2026, 1, 5, 12, 1)},
		"every five minutes":       {"*/5 * * * *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 7), at(time.UTC, 2026, 1, 5, 12, 10)},
		"daily at three":           {"0 3 * * *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 1, 6, 3, 0)},
		"later today":              {"30 18 * * *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 1, 5, 18, 30)},
		"hourly":                   {"@hourly", time.UTC, at(time.UTC, 2026, 1, 5, 12, 30), at(time.UTC, 2026, 1, 5, 13, 0)},
		"weekly is sunday":         {"@weekly", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 1, 11, 0, 0)},
		"monthly":                  {"@monthly", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 2, 1, 0, 0)},
		"yearly":                   {"@yearly", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2027, 1, 1, 0, 0)},
		"weekdays at nine":         {"0 9 * * mon-fri", time.UTC, at(time.UTC, 2026, 1, 9, 10, 0), at(time.UTC, 2026, 1, 12, 9, 0)},
		"list of days":             {"0 0 * * 1,3,5", time.UTC, at(time.UTC, 2026, 1, 6, 0, 0), at(time.UTC, 2026, 1, 7, 0, 0)},
		"sunday as 7":              {"0 0 * * 7", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 1, 11, 0, 0)},
		"day or weekday":           {"0 0 13 * fri", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 1, 9, 0, 0)},
		"month names":              {"0 0 1 jun *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2026, 6, 1, 0, 0)},
		"february 29th":            {"0 0 29 2 *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 0), at(time.UTC, 2028, 2, 29, 0, 0)},
		"range with step":          {"0-30/10 * * * *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 25), at(time.UTC, 2026, 1, 5, 12, 30)},
		"start with step":          {"15/20 * * * *", time.UTC, at(time.UTC, 2026, 1, 5, 12, 20), at(time.UTC, 2026, 1, 5, 12, 35)},
		"in a time zone":           {"0 3 * * *", amsterdam, at(time.UTC, 2026, 1, 5, 12, 0), at(amsterdam, 2026, 1, 6, 3, 0)},
		"across summer time":       {"0 12 * * *", amsterdam, at(amsterdam, 2026, 3, 28, 13, 0), at(amsterdam, 2026, 3, 29, 12, 0)},
		"every 5m is aligned":      {"@every 5m", time.UTC, at(time.UTC, 2026, 1, 5, 12, 7), at(time.UTC, 2026, 1, 5, 12, 10)},
		"every 5m from a boundary": {"@every 5m", time.UTC, at(time.UTC, 2026, 1, 5, 12, 10), at(time.UTC, 2026, 1, 5, 12, 15)},
	} {
		s, err := parseSchedule(tt.spec, tt.loc)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := s.next(tt.from); !got.Equal(tt.want) {
			t.Errorf("%s: next(%s) = %s, want %s", name, tt.from, got, tt.want)
		}
	}
}

func TestParseScheduleErrors(t *testing.T) {
	for spec, want := range map[string]string{
		"":              "five fields",
		"* * * *":       "five fields",
		"* * * * * *":   "five fields",
		"60 * * * *":    "outside the minute field",
		"* 24 * * *":    "outside the hour field",
		"* * 0 * *":     "outside the day of the month field",
		"* * * 13 *":    "outside the month field",
		"* * * * 8":     "outside the day of the week field",
		"*/0 * * * *":   "step",
		"*/x * * * *":   "step",
		"5-1 * * * *":   "backwards",
		"a * * * *":     "not a valid value",
		"@every":        "five fields",
		"@every soon":   "interval",
		"@every 100ms":  "interval",
		"@sometimes":    "five fields",
		"* * * foo *":   "not a valid value",
		"1-x * * * *":   "not a valid value",
		"* * * * mon-x": "not a valid value",
	} {
		if _, err := parseSchedule(spec, time.UTC); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", spec, err, want)
		}
	}
}

func TestNextGivesUpOnImpossibleDates(t *testing.T) {
	s, err := parseSchedule("0 0 31 2 *", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Errorf("next = %s, want none", got)
	}
}
