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
import { useEffect, useSyncExternalStore } from 'react'

import { getHolidayCalendar } from '../api'
import {
  isHolidayCalendarLoaded,
  setHolidayCalendar,
} from '../lib/billing-expression/holiday-calendar'
import { compileBillingExpression } from '../lib/billing-expression/parser'
import {
  TIME_DEPENDENT_FUNCTIONS,
  WORKDAY_FUNCTION,
} from '../lib/billing-expression/types'

const listeners = new Set<() => void>()
let timestamp = Date.now()
let timer: ReturnType<typeof setTimeout> | undefined

function refreshBillingTime(): void {
  timestamp = Date.now()
  listeners.forEach((listener) => listener())
  if (timer) clearTimeout(timer)
  if (listeners.size > 0 && document.visibilityState !== 'hidden') {
    timer = setTimeout(refreshBillingTime, 60000 - (Date.now() % 60000))
  }
}

function subscribeBillingTime(listener: () => void): () => void {
  listeners.add(listener)
  if (listeners.size === 1) {
    document.addEventListener('visibilitychange', refreshBillingTime)
    refreshBillingTime()
  }
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) {
      if (timer) clearTimeout(timer)
      timer = undefined
      document.removeEventListener('visibilitychange', refreshBillingTime)
    }
  }
}

let holidayCalendarRequest: Promise<void> | null = null

/** Loads the server calendar once; until then is_workday() previews stay unresolved. */
function loadHolidayCalendar(): void {
  if (holidayCalendarRequest || isHolidayCalendarLoaded()) return
  holidayCalendarRequest = getHolidayCalendar()
    .then((payload) => {
      setHolidayCalendar(payload)
      refreshBillingTime()
    })
    .catch(() => {
      // Previews fall back to listing every tier; the next mount retries.
      holidayCalendarRequest = null
    })
}

function getBillingTime(): number {
  return timestamp
}
function subscribeWithoutTime(): () => void {
  return () => {}
}
function getNoTime(): undefined {
  return undefined
}

/** One minute clock for active time-dependent previews; never used by settlement logs. */
export function useBillingTime(
  expression: string | null | undefined,
  enabled = true
): number | undefined {
  const compiled =
    expression && enabled ? compileBillingExpression(expression) : null
  const needsTime =
    compiled?.status === 'ready' &&
    TIME_DEPENDENT_FUNCTIONS.some((name) => compiled.functions.has(name))
  const needsHolidayCalendar =
    compiled?.status === 'ready' && compiled.functions.has(WORKDAY_FUNCTION)
  useEffect(() => {
    if (needsHolidayCalendar) loadHolidayCalendar()
  }, [needsHolidayCalendar])
  return useSyncExternalStore(
    needsTime ? subscribeBillingTime : subscribeWithoutTime,
    needsTime ? getBillingTime : getNoTime,
    getNoTime
  )
}
