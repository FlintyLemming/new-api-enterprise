package cnholiday_test

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/pkg/cnholiday"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 2026 entry as published by https://api.comm.miui.com/holiday/holiday.jsp.
const xiaomi2026 = `{"versioncode":25,"holiday":[{"year":2026,"workday":[4,45,59,129,263,283],"freeday":[1,2,3,46,47,48,49,50,51,52,53,54,94,95,96,121,122,123,124,125,170,171,172,268,269,270,274,275,276,277,278,279,280]}]}`

func TestCalendarIsWorkdayFollowsOfficialAdjustments(t *testing.T) {
	calendar, err := cnholiday.Parse([]byte(xiaomi2026))
	require.NoError(t, err)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	cases := []struct {
		name string
		date time.Time
		want bool
	}{
		{"new year holiday on Thursday", time.Date(2026, 1, 1, 10, 0, 0, 0, shanghai), false},
		{"adjusted workday on Sunday", time.Date(2026, 1, 4, 10, 0, 0, 0, shanghai), true},
		{"adjusted workday on Saturday before spring festival", time.Date(2026, 2, 14, 10, 0, 0, 0, shanghai), true},
		{"spring festival on Monday", time.Date(2026, 2, 16, 10, 0, 0, 0, shanghai), false},
		{"ordinary Monday", time.Date(2026, 3, 2, 10, 0, 0, 0, shanghai), true},
		{"ordinary Saturday", time.Date(2026, 3, 7, 10, 0, 0, 0, shanghai), false},
		{"uncovered year falls back to Monday-Friday", time.Date(2030, 1, 1, 10, 0, 0, 0, shanghai), true},
		{"uncovered year weekend", time.Date(2030, 1, 5, 10, 0, 0, 0, shanghai), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, calendar.IsWorkday(tc.date))
		})
	}
}

func TestCalendarUsesDateInValueLocation(t *testing.T) {
	calendar, err := cnholiday.Parse([]byte(xiaomi2026))
	require.NoError(t, err)
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	// 2026-01-04 01:00 in Shanghai is still 2026-01-03 (a holiday) in UTC.
	instant := time.Date(2026, 1, 4, 1, 0, 0, 0, shanghai)
	assert.True(t, calendar.IsWorkday(instant))
	assert.False(t, calendar.IsWorkday(instant.UTC()))
}

func TestNilCalendarUsesMondayToFriday(t *testing.T) {
	var calendar *cnholiday.Calendar
	assert.True(t, calendar.IsWorkday(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	assert.False(t, calendar.IsWorkday(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)))
}

func TestParseRejectsInvalidCalendars(t *testing.T) {
	cases := map[string]string{
		"no years":            `{"holiday":[]}`,
		"not json":            `<html>`,
		"day beyond year end": `{"holiday":[{"year":2026,"freeday":[366],"workday":[]}]}`,
		"day zero":            `{"holiday":[{"year":2026,"freeday":[0],"workday":[]}]}`,
		"conflicting day":     `{"holiday":[{"year":2026,"freeday":[45],"workday":[45]}]}`,
		"duplicated year":     `{"holiday":[{"year":2026,"freeday":[1]},{"year":2026,"freeday":[2]}]}`,
		"year out of range":   `{"holiday":[{"year":1969,"freeday":[1]}]}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := cnholiday.Parse([]byte(payload))
			assert.Error(t, err)
		})
	}
}

func TestParseAcceptsLeapDay366(t *testing.T) {
	calendar, err := cnholiday.Parse([]byte(`{"holiday":[{"year":2028,"freeday":[366],"workday":[]}]}`))
	require.NoError(t, err)
	assert.False(t, calendar.IsWorkday(time.Date(2028, 12, 31, 12, 0, 0, 0, time.UTC)))
}

func TestStoredCalendarIsCanonicalAndReloadable(t *testing.T) {
	calendar, err := cnholiday.Parse([]byte(`{"versioncode":1,"holiday":[{"year":2027,"freeday":[3,1,1],"workday":[31]},{"year":2026,"freeday":[1],"workday":[]}]}`))
	require.NoError(t, err)
	stored, err := calendar.Marshal()
	require.NoError(t, err)
	assert.JSONEq(t, `{"holiday":[{"year":2026,"workday":[],"freeday":[1]},{"year":2027,"workday":[31],"freeday":[1,3]}]}`, string(stored))
	assert.Equal(t, []int{2026, 2027}, calendar.Years())

	t.Cleanup(func() { cnholiday.SetCurrent(nil) })
	require.NoError(t, cnholiday.Load(string(stored)))
	assert.True(t, cnholiday.Current().Covers(2027))
	assert.False(t, cnholiday.IsWorkday(time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)))

	require.NoError(t, cnholiday.Load(""))
	assert.Nil(t, cnholiday.Current())
}
