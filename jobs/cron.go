package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// schedule says when something runs: a cron expression or a fixed interval.
type schedule struct {
	every                         time.Duration
	minute, hour, dom, month, dow bitset
	domAny, dowAny                bool
	loc                           *time.Location
}

type bitset uint64

func (b bitset) has(n int) bool { return b&(1<<uint(n)) != 0 }

type fieldSpec struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	minuteField = fieldSpec{name: "minute", min: 0, max: 59}
	hourField   = fieldSpec{name: "hour", min: 0, max: 23}
	domField    = fieldSpec{name: "day of the month", min: 1, max: 31}
	monthField  = fieldSpec{name: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	dowField = fieldSpec{name: "day of the week", min: 0, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

var descriptors = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// parseSchedule reads "minute hour day-of-month month day-of-week", the shortcuts @hourly,
// @daily, @weekly, @monthly and @yearly, or "@every <duration>".
func parseSchedule(spec string, loc *time.Location) (*schedule, error) {
	spec = strings.TrimSpace(spec)
	if rest, ok := strings.CutPrefix(spec, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil || d < time.Second {
			return nil, fmt.Errorf("jobs: %q is not an interval of at least a second, such as @every 5m", spec)
		}
		return &schedule{every: d, loc: loc}, nil
	}
	if expanded, ok := descriptors[spec]; ok {
		spec = expanded
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf("jobs: %q needs five fields: minute hour day-of-month month day-of-week", spec)
	}
	s := &schedule{loc: loc}
	var err error
	for i, target := range []struct {
		spec fieldSpec
		bits *bitset
	}{{minuteField, &s.minute}, {hourField, &s.hour}, {domField, &s.dom}, {monthField, &s.month}, {dowField, &s.dow}} {
		if *target.bits, err = parseField(fields[i], target.spec); err != nil {
			return nil, fmt.Errorf("jobs: %q: %w", spec, err)
		}
	}
	// Sunday is 0 and 7.
	if s.dow.has(7) {
		s.dow |= 1
	}
	s.domAny, s.dowAny = fields[2] == "*" || fields[2] == "?", fields[4] == "*" || fields[4] == "?"
	return s, nil
}

func parseField(field string, spec fieldSpec) (bitset, error) {
	var bits bitset
	for _, part := range strings.Split(field, ",") {
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("the step %q in the %s field is not a positive number", stepPart, spec.name)
			}
			step = n
		}
		lo, hi := spec.min, spec.max
		if spec.name == "day of the week" {
			hi = 6
		}
		switch {
		case rangePart == "*" || rangePart == "?":
		case strings.Contains(rangePart, "-"):
			from, to, _ := strings.Cut(rangePart, "-")
			var err error
			if lo, err = parseValue(from, spec); err != nil {
				return 0, err
			}
			if hi, err = parseValue(to, spec); err != nil {
				return 0, err
			}
		default:
			n, err := parseValue(rangePart, spec)
			if err != nil {
				return 0, err
			}
			lo, hi = n, n
			if hasStep {
				hi = spec.max
			}
		}
		if lo > hi {
			return 0, fmt.Errorf("the range %q in the %s field runs backwards", rangePart, spec.name)
		}
		for n := lo; n <= hi; n += step {
			bits |= 1 << uint(n)
		}
	}
	return bits, nil
}

func parseValue(s string, spec fieldSpec) (int, error) {
	if n, ok := spec.names[strings.ToLower(s)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid value for the %s field", s, spec.name)
	}
	if n < spec.min || n > spec.max {
		return 0, fmt.Errorf("%d is outside the %s field, which runs from %d to %d", n, spec.name, spec.min, spec.max)
	}
	return n, nil
}

// next returns the first moment after t that matches, or the zero time when none does in the
// coming years. Intervals are aligned to the Unix epoch, so that every process agrees on them.
func (s *schedule) next(t time.Time) time.Time {
	if s.every > 0 {
		step := s.every.Nanoseconds()
		return time.Unix(0, (t.UnixNano()/step+1)*step)
	}
	t = t.In(s.loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case !s.month.has(int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, s.loc)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, s.loc)
		case !s.hour.has(t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, s.loc)
		case !s.minute.has(t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

// dayMatches follows cron: when both the day of the month and of the week are restricted, either
// one is enough.
func (s *schedule) dayMatches(t time.Time) bool {
	dom, dow := s.dom.has(t.Day()), s.dow.has(int(t.Weekday()))
	if s.domAny || s.dowAny {
		return dom && dow
	}
	return dom || dow
}
