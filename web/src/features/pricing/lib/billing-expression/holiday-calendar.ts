/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { BillingExpressionError } from './types'

type HolidayYear = { free: Set<number>; work: Set<number> }
type StoredHolidayYear = {
  year?: unknown
  freeday?: unknown
  workday?: unknown
}

// null until the server calendar has been read; previews then leave
// is_workday() unresolved instead of guessing.
let calendar: Map<number, HolidayYear> | null = null

function dayList(value: unknown): number[] {
  if (!Array.isArray(value)) return []
  return value.filter(
    (day): day is number => typeof day === 'number' && Number.isInteger(day)
  )
}

/**
 * Installs the server's stored calendar (`{"holiday": [...]}`, the same
 * document billing reads). An empty payload means the server has no calendar,
 * so every date follows Monday–Friday exactly as billing does.
 */
export function setHolidayCalendar(payload: string): void {
  const years = new Map<number, HolidayYear>()
  if (payload.trim()) {
    const document = JSON.parse(payload) as { holiday?: unknown }
    const entries = Array.isArray(document.holiday) ? document.holiday : []
    for (const entry of entries as StoredHolidayYear[]) {
      if (typeof entry.year !== 'number') continue
      years.set(entry.year, {
        free: new Set(dayList(entry.freeday)),
        work: new Set(dayList(entry.workday)),
      })
    }
  }
  calendar = years
}

/** Test hook: forget the server calendar. */
export function clearHolidayCalendar(): void {
  calendar = null
}

export function isHolidayCalendarLoaded(): boolean {
  return calendar !== null
}

/** Mirrors cnholiday.Calendar.IsWorkday for a date already resolved in the billing timezone. */
export function isWorkdayOn(date: {
  year: number
  month: number
  day: number
  weekday: number
}): boolean {
  if (!calendar) {
    throw new BillingExpressionError({
      code: 'unsupported',
      detail: 'holiday calendar',
    })
  }
  const dayOfYear =
    (Date.UTC(date.year, date.month - 1, date.day) -
      Date.UTC(date.year, 0, 1)) /
      86_400_000 +
    1
  const year = calendar.get(date.year)
  if (year?.free.has(dayOfYear)) return false
  if (year?.work.has(dayOfYear)) return true
  return date.weekday !== 0 && date.weekday !== 6
}
