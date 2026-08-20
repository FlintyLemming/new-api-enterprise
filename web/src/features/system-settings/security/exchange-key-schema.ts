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
import * as z from 'zod'

import type { ExchangeKeySettingsView } from './exchange-key-api'

const MIN_SECRET_LENGTH = 16

const exchangeKeyFormObject = z.object({
  enabled: z.boolean(),
  secret_key: z.string(),
  secret_key_clear: z.boolean(),
})

export function exchangeKeyFormSchema(view: ExchangeKeySettingsView) {
  return exchangeKeyFormObject.superRefine((values, ctx) => {
    if (
      values.secret_key !== '' &&
      values.secret_key.length < MIN_SECRET_LENGTH
    ) {
      ctx.addIssue({
        code: 'custom',
        path: ['secret_key'],
        message: 'Secret must be at least 16 characters',
      })
    }
    if (values.secret_key_clear && values.enabled) {
      ctx.addIssue({
        code: 'custom',
        path: ['secret_key_clear'],
        message: 'Disable Exchange Key before clearing the secret',
      })
    }
    if (values.enabled && !view.secret_configured && values.secret_key === '') {
      ctx.addIssue({
        code: 'custom',
        path: ['secret_key'],
        message: 'Enter a secret before enabling Exchange Key',
      })
    }
  })
}

export type ExchangeKeyFormValues = z.output<typeof exchangeKeyFormObject>
