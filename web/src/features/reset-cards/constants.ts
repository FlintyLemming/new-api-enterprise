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

import type { StatusBadgeProps } from '@/components/status-badge'

// ============================================================================
// Reset Card Status Configuration
// ============================================================================

export const RESET_CARD_STATUS = {
  UNUSED: 1,
  USED: 2,
  DISABLED: 3,
} as const

// labelKey values are i18n keys; use t(config.labelKey) in components
export const RESET_CARD_STATUSES: Record<
  number,
  Pick<StatusBadgeProps, 'variant'> & {
    labelKey: string
    value: number
  }
> = {
  [RESET_CARD_STATUS.UNUSED]: {
    labelKey: 'Unused',
    variant: 'success',
    value: RESET_CARD_STATUS.UNUSED,
  },
  [RESET_CARD_STATUS.USED]: {
    labelKey: 'Used',
    variant: 'neutral',
    value: RESET_CARD_STATUS.USED,
  },
  [RESET_CARD_STATUS.DISABLED]: {
    labelKey: 'Disabled',
    variant: 'neutral',
    value: RESET_CARD_STATUS.DISABLED,
  },
} as const

// Virtual status filter value for expired cards
// Note: "Expired" is not a real DB status, it's computed from expired_time
export const RESET_CARD_FILTER_EXPIRED = 'expired'

export const RESET_CARD_FILTER_VALUES = [
  String(RESET_CARD_STATUS.UNUSED),
  String(RESET_CARD_STATUS.USED),
  String(RESET_CARD_STATUS.DISABLED),
  RESET_CARD_FILTER_EXPIRED,
] as const

export function getResetCardStatusOptions(t: TFunction) {
  return [
    ...Object.values(RESET_CARD_STATUSES).map((config) => ({
      label: t(config.labelKey),
      value: String(config.value),
    })),
    {
      label: t('Expired'),
      value: RESET_CARD_FILTER_EXPIRED,
    },
  ]
}

// ============================================================================
// Validation Constants
// ============================================================================

export const RESET_CARD_VALIDATION = {
  NAME_MIN_LENGTH: 1,
  NAME_MAX_LENGTH: 50,
  COUNT_MIN: 1,
  COUNT_MAX: 100,
} as const

// ============================================================================
// Error Messages (i18n keys; use t(ERROR_MESSAGES.xxx) when displaying)
// ============================================================================

export const ERROR_MESSAGES = {
  LOAD_FAILED: 'Failed to load reset cards',
  NAME_LENGTH_INVALID: 'Reset card name length must be between 1-50',
  COUNT_INVALID: 'Count must be between 1 and 100',
  USER_REQUIRED: 'Please select a user',
} as const

export function getResetCardFormErrorMessages(t: TFunction) {
  return {
    NAME_LENGTH_INVALID: t(ERROR_MESSAGES.NAME_LENGTH_INVALID),
    COUNT_INVALID: t(ERROR_MESSAGES.COUNT_INVALID),
    USER_REQUIRED: t(ERROR_MESSAGES.USER_REQUIRED),
  } as const
}

// ============================================================================
// Success Messages (i18n keys; use t(SUCCESS_MESSAGES.xxx) when displaying)
// ============================================================================

export const SUCCESS_MESSAGES = {
  RESET_CARDS_GRANTED: 'Reset cards granted successfully',
  RESET_CARD_DISABLED: 'Reset card disabled',
  RESET_CARD_DELETED: 'Reset card deleted',
} as const
