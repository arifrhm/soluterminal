package deepseekusage

import "time"

// DeepSeek documents its peak windows in Beijing time. Public-holiday dates
// are consequently interpreted in Asia/Shanghai as well. China has not used
// daylight-saving time during the covered years, but using the IANA location
// keeps the policy explicit rather than baking an incidental UTC offset into
// the calendar.
const (
	PolicyTimezone       = "Asia/Shanghai"
	CalendarVersion      = "cn-public-holidays-2025-2026-v1"
	CalendarUpdatedDate  = "2025-11-04"
	CalendarCoverageFrom = 2025
	CalendarCoverageTo   = 2026
	ScheduleSourceURL    = "https://api-docs.deepseek.com/quick_start/pricing/"
)

type Source struct {
	Label      string
	URL        string
	IssuedDate string
}

var calendarSources = []Source{
	{Label: "State Council 2025 public-holiday notice", URL: "https://www.gov.cn/zhengce/zhengceku/202411/content_6986383.htm", IssuedDate: "2024-11-12"},
	{Label: "State Council 2026 public-holiday notice", URL: "https://www.gov.cn/zhengce/zhengceku/202511/content_7047091.htm", IssuedDate: "2025-11-04"},
}

// ScheduleState is deliberately tri-state. Outside the explicitly maintained
// holiday calendar we can still describe the weekday/hour rule, but cannot
// claim a billing state or transition that a later holiday notice could alter.
type ScheduleState string

const (
	SchedulePeak    ScheduleState = "peak"
	ScheduleOffPeak ScheduleState = "off-peak"
	ScheduleOnly    ScheduleState = "schedule-only"
)

type ScheduleResult struct {
	State             ScheduleState
	HolidayVerified   bool
	Holiday           bool
	ScheduleWouldPeak bool
}

type ScheduleTransition struct {
	State ScheduleState
	At    time.Time
}

var shanghaiLocation = func() *time.Location {
	loc, err := time.LoadLocation(PolicyTimezone)
	if err != nil {
		// Go distributions include Asia/Shanghai. UTC+8 is a deterministic
		// fallback for the covered years if a minimal host omits tzdata.
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}()

// Explicit official closure dates. Do not replace this with a lunar-calendar
// calculation: the State Council notices, including adjusted ranges, are the
// source of truth. Weekend entries are retained so this table mirrors those
// notices exactly even though weekends are independently off-peak.
var publicHolidayClosures = func() map[string]struct{} {
	out := map[string]struct{}{}
	add := func(start string, days int) {
		at, err := time.ParseInLocation("2006-01-02", start, shanghaiLocation)
		if err != nil {
			panic(err)
		}
		for i := 0; i < days; i++ {
			out[at.AddDate(0, 0, i).Format("2006-01-02")] = struct{}{}
		}
	}
	add("2025-01-01", 1)
	add("2025-01-28", 8)
	add("2025-04-04", 3)
	add("2025-05-01", 5)
	add("2025-05-31", 3)
	add("2025-10-01", 8)
	add("2026-01-01", 3)
	add("2026-02-15", 9)
	add("2026-04-04", 3)
	add("2026-05-01", 5)
	add("2026-06-19", 3)
	add("2026-09-25", 3)
	add("2026-10-01", 7)
	return out
}()

func CalendarSources() []Source {
	out := make([]Source, len(calendarSources))
	copy(out, calendarSources)
	return out
}

func scheduleWindowPeak(local time.Time) bool {
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
		return false
	}
	minute := local.Hour()*60 + local.Minute()
	return (minute >= 9*60 && minute < 12*60) || (minute >= 14*60 && minute < 18*60)
}

func calendarCovered(local time.Time) bool {
	return local.Year() >= CalendarCoverageFrom && local.Year() <= CalendarCoverageTo
}

// ScheduleAt resolves the canonical DeepSeek rate state at an explicit clock
// instant. Weekends stay off-peak even when a State Council notice designates
// them as make-up workdays because DeepSeek's policy is weekday based.
func ScheduleAt(at time.Time) ScheduleResult {
	local := at.In(shanghaiLocation)
	wouldPeak := scheduleWindowPeak(local)
	if !calendarCovered(local) {
		return ScheduleResult{State: ScheduleOnly, ScheduleWouldPeak: wouldPeak}
	}
	_, holiday := publicHolidayClosures[local.Format("2006-01-02")]
	state := ScheduleOffPeak
	if wouldPeak && !holiday {
		state = SchedulePeak
	}
	return ScheduleResult{State: state, HolidayVerified: true, Holiday: holiday, ScheduleWouldPeak: wouldPeak}
}

// NextTransition returns the first provable peak/off-peak change after at.
// The search is bounded by the maintained calendar. If finding the next
// change would cross that boundary, no transition is returned.
func NextTransition(at time.Time) (ScheduleTransition, bool) {
	current := ScheduleAt(at)
	if !current.HolidayVerified {
		return ScheduleTransition{}, false
	}
	local := at.In(shanghaiLocation)
	end := time.Date(CalendarCoverageTo+1, time.January, 1, 0, 0, 0, 0, shanghaiLocation)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, shanghaiLocation)
	for day.Before(end) {
		for _, hm := range [][2]int{{9, 0}, {12, 0}, {14, 0}, {18, 0}} {
			candidate := time.Date(day.Year(), day.Month(), day.Day(), hm[0], hm[1], 0, 0, shanghaiLocation)
			if !candidate.After(local) || !candidate.Before(end) {
				continue
			}
			result := ScheduleAt(candidate)
			if result.HolidayVerified && result.State != current.State {
				return ScheduleTransition{State: result.State, At: candidate}, true
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return ScheduleTransition{}, false
}
