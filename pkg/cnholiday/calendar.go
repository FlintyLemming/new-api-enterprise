// Package cnholiday decides whether a date is a working day under the
// mainland China public holiday schedule, including adjusted working days
// (调休) that fall on weekends.
package cnholiday

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// OptionKey is the options-table key holding the canonical calendar JSON.
const OptionKey = "HolidayCalendar"

// Load parses a stored calendar and makes it current. An empty value clears
// the calendar, leaving only the Monday–Friday rule.
func Load(value string) error {
	if value == "" {
		SetCurrent(nil)
		return nil
	}
	calendar, err := Parse([]byte(value))
	if err != nil {
		return err
	}
	SetCurrent(calendar)
	return nil
}

// Year lists one calendar year's official adjustments as 1-based day-of-year
// numbers, the format published by the Xiaomi holiday API.
type Year struct {
	Year     int   `json:"year"`
	WorkDays []int `json:"workday"`
	FreeDays []int `json:"freeday"`
}

type yearDays struct {
	free map[int]bool
	work map[int]bool
}

// Calendar is an immutable, validated holiday schedule.
type Calendar struct {
	years   map[int]yearDays
	payload []Year
}

// Parse validates a {"holiday": [...]} document. It rejects documents that
// carry no years so that a broken upstream response cannot replace a good
// calendar with an empty one.
func Parse(data []byte) (*Calendar, error) {
	var document struct {
		Holiday []Year `json:"holiday"`
	}
	if err := common.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode holiday calendar: %w", err)
	}
	if len(document.Holiday) == 0 {
		return nil, errors.New("holiday calendar has no years")
	}
	calendar := &Calendar{years: make(map[int]yearDays, len(document.Holiday))}
	for _, year := range document.Holiday {
		if year.Year < 1970 || year.Year > 9999 {
			return nil, fmt.Errorf("holiday calendar year %d is out of range", year.Year)
		}
		if _, exists := calendar.years[year.Year]; exists {
			return nil, fmt.Errorf("holiday calendar year %d is duplicated", year.Year)
		}
		daysInYear := time.Date(year.Year, time.December, 31, 0, 0, 0, 0, time.UTC).YearDay()
		days := yearDays{free: map[int]bool{}, work: map[int]bool{}}
		for _, day := range year.FreeDays {
			if day < 1 || day > daysInYear {
				return nil, fmt.Errorf("holiday calendar year %d has invalid day %d", year.Year, day)
			}
			days.free[day] = true
		}
		for _, day := range year.WorkDays {
			if day < 1 || day > daysInYear {
				return nil, fmt.Errorf("holiday calendar year %d has invalid day %d", year.Year, day)
			}
			if days.free[day] {
				return nil, fmt.Errorf("holiday calendar year %d lists day %d as both holiday and workday", year.Year, day)
			}
			days.work[day] = true
		}
		calendar.years[year.Year] = days
		calendar.payload = append(calendar.payload, Year{
			Year:     year.Year,
			WorkDays: sortedDays(days.work),
			FreeDays: sortedDays(days.free),
		})
	}
	slices.SortFunc(calendar.payload, func(a, b Year) int { return a.Year - b.Year })
	return calendar, nil
}

func sortedDays(days map[int]bool) []int {
	result := make([]int, 0, len(days))
	for day := range days {
		result = append(result, day)
	}
	slices.Sort(result)
	return result
}

// Marshal returns the canonical stored form: years in ascending order with
// sorted, de-duplicated days and no upstream-only fields.
func (c *Calendar) Marshal() ([]byte, error) {
	return common.Marshal(map[string][]Year{"holiday": c.payload})
}

// Years returns the covered years in ascending order.
func (c *Calendar) Years() []int {
	if c == nil {
		return nil
	}
	years := make([]int, 0, len(c.payload))
	for _, year := range c.payload {
		years = append(years, year.Year)
	}
	return years
}

// Covers reports whether the calendar has the official schedule for the year.
func (c *Calendar) Covers(year int) bool {
	if c == nil {
		return false
	}
	_, ok := c.years[year]
	return ok
}

// IsWorkday evaluates the date of value in value's own location. A listed
// holiday is never a workday, a listed adjusted workday always is, and every
// other date (including all dates of uncovered years) follows Monday–Friday.
func (c *Calendar) IsWorkday(value time.Time) bool {
	if c != nil {
		if days, ok := c.years[value.Year()]; ok {
			if days.free[value.YearDay()] {
				return false
			}
			if days.work[value.YearDay()] {
				return true
			}
		}
	}
	weekday := value.Weekday()
	return weekday != time.Saturday && weekday != time.Sunday
}

var current atomic.Pointer[Calendar]

// Current returns the calendar used by billing, or nil before one is loaded.
func Current() *Calendar {
	return current.Load()
}

// SetCurrent replaces the calendar used by billing.
func SetCurrent(calendar *Calendar) {
	current.Store(calendar)
}

// IsWorkday evaluates value against the current calendar.
func IsWorkday(value time.Time) bool {
	return Current().IsWorkday(value)
}
