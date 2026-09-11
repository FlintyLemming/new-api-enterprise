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
import type { TFunction } from 'i18next'
import { z } from 'zod'

import {
  RESET_CARD_VALIDATION,
  getResetCardFormErrorMessages,
} from '../constants'
import type { GrantResetCardsPayload } from '../types'

export function getResetCardGrantFormSchema(t: TFunction) {
  const msg = getResetCardFormErrorMessages(t)
  return z.object({
    user_id: z.number().min(1, msg.USER_REQUIRED),
    name: z
      .string()
      .min(RESET_CARD_VALIDATION.NAME_MIN_LENGTH, msg.NAME_LENGTH_INVALID)
      .max(RESET_CARD_VALIDATION.NAME_MAX_LENGTH, msg.NAME_LENGTH_INVALID),
    count: z
      .number()
      .int()
      .min(RESET_CARD_VALIDATION.COUNT_MIN, msg.COUNT_INVALID)
      .max(RESET_CARD_VALIDATION.COUNT_MAX, msg.COUNT_INVALID),
    expired_time: z.date().optional(),
  })
}

export type ResetCardGrantFormValues = {
  user_id: number
  name: string
  count: number
  expired_time?: Date
}

export const RESET_CARD_GRANT_DEFAULT_VALUES: ResetCardGrantFormValues = {
  user_id: 0,
  name: '',
  count: 1,
  expired_time: undefined,
}

export function transformGrantFormToPayload(
  data: ResetCardGrantFormValues
): GrantResetCardsPayload {
  return {
    user_id: data.user_id,
    name: data.name,
    count: data.count,
    expired_time: data.expired_time
      ? Math.floor(data.expired_time.getTime() / 1000)
      : 0,
  }
}
