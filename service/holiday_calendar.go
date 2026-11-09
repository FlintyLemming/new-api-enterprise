package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/cnholiday"
)

const (
	defaultHolidayCalendarURL  = "https://api.comm.miui.com/holiday/holiday.jsp"
	holidayCalendarFetchLimit  = 1 << 20
	holidayCalendarFetchWindow = 30 * time.Second
)

// HolidayCalendarSyncResult is recorded on the system task row.
type HolidayCalendarSyncResult struct {
	Years   []int `json:"years"`
	Changed bool  `json:"changed"`
}

// SyncHolidayCalendar fetches the mainland China holiday schedule and stores it
// in the options table, from which every node loads it. A failed fetch keeps
// the previously stored calendar.
func SyncHolidayCalendar(ctx context.Context) (*HolidayCalendarSyncResult, error) {
	endpoint := common.GetEnvOrDefaultString("HOLIDAY_CALENDAR_URL", defaultHolidayCalendarURL)
	ctx, cancel := context.WithTimeout(ctx, holidayCalendarFetchWindow)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build holiday calendar request: %w", err)
	}
	response, err := GetHttpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch holiday calendar: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("fetch holiday calendar: unexpected status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, holidayCalendarFetchLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read holiday calendar: %w", err)
	}
	if len(body) > holidayCalendarFetchLimit {
		return nil, fmt.Errorf("holiday calendar response exceeds %d bytes", holidayCalendarFetchLimit)
	}
	calendar, err := cnholiday.Parse(body)
	if err != nil {
		return nil, err
	}
	stored, err := calendar.Marshal()
	if err != nil {
		return nil, err
	}

	result := &HolidayCalendarSyncResult{Years: calendar.Years()}
	common.OptionMapRWMutex.RLock()
	previous := common.OptionMap[cnholiday.OptionKey]
	common.OptionMapRWMutex.RUnlock()
	if previous == string(stored) {
		return result, nil
	}
	if err := model.UpdateOption(cnholiday.OptionKey, string(stored)); err != nil {
		return nil, fmt.Errorf("store holiday calendar: %w", err)
	}
	result.Changed = true
	return result, nil
}
