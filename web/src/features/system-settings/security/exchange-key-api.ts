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
import { api } from '@/lib/api'

// Exchange Key is read and written through its own endpoint instead of the
// generic option API: the secret never appears in any response, and the
// generic endpoint rejects exchange_key.* for the same reason.

export const EXCHANGE_KEY_SETTINGS_QUERY_KEY = [
  'exchange-key-settings',
] as const

export interface ExchangeKeySettingsView {
  enabled: boolean
  secret_configured: boolean
  secret_from_env: boolean
  enabled_from_env: boolean
}

export interface ExchangeKeySettingsUpdate {
  enabled: boolean
  /** Empty keeps the stored secret; the UI never receives the current value. */
  secret_key: string
  /** Only accepted by the backend when the result stays disabled. */
  secret_key_clear: boolean
}

type ExchangeKeySettingsResponse = {
  success: boolean
  message?: string
  data?: ExchangeKeySettingsView
}

type ExchangeKeyUpdateResponse = {
  success: boolean
  message?: string
}

export async function fetchExchangeKeySettings(): Promise<ExchangeKeySettingsView> {
  const res = await api.get<ExchangeKeySettingsResponse>(
    '/api/option/exchange-key'
  )
  if (!res.data.success || !res.data.data) {
    throw new Error(res.data.message || 'Failed to load Exchange Key settings')
  }
  return res.data.data
}

export async function updateExchangeKeySettings(
  payload: ExchangeKeySettingsUpdate
): Promise<void> {
  const res = await api.put<ExchangeKeyUpdateResponse>(
    '/api/option/exchange-key',
    payload
  )
  if (!res.data.success) {
    throw new Error(
      res.data.message || 'Failed to update Exchange Key settings'
    )
  }
}
