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

// Langfuse is read and written through its own endpoint instead of the generic
// option API: the backend saves the whole group in one transaction, because a
// per-key write could pair a new host with an old key and break a live
// exporter. The generic endpoint rejects langfuse_setting.* for the same
// reason, and the secret key never appears in any response.

export const LANGFUSE_SETTINGS_QUERY_KEY = ['langfuse-settings'] as const

export interface LangfuseSettingsView {
  enabled: boolean
  host: string
  public_key: string
  secret_key_configured: boolean
  environment: string
  sample_rate: number
  send_content: boolean
  max_content_bytes: number
  max_response_bytes: number
  max_in_flight_capture_bytes: number
  max_session_body_bytes: number
  session_header_names: string[]
  session_body_paths: string[]
  queue_size: number
  batch_size: number
  flush_interval_seconds: number
}

export interface LangfuseSettingsUpdate extends Omit<
  LangfuseSettingsView,
  'secret_key_configured'
> {
  /** Empty keeps the stored secret; the UI never receives the current value. */
  secret_key: string
  /** Only accepted by the backend when the result stays disabled. */
  secret_key_clear: boolean
}

type LangfuseSettingsResponse = {
  success: boolean
  message?: string
  data?: LangfuseSettingsView
}

type LangfuseUpdateResponse = {
  success: boolean
  message?: string
}

export async function fetchLangfuseSettings(): Promise<LangfuseSettingsView> {
  const res = await api.get<LangfuseSettingsResponse>('/api/option/langfuse')
  if (!res.data.success || !res.data.data) {
    throw new Error(res.data.message || 'Failed to load Langfuse settings')
  }
  return res.data.data
}

// Resolves without a payload: the backend answers a successful PUT with no
// data, so callers refetch the view to pick up the persisted result.
export async function updateLangfuseSettings(
  payload: LangfuseSettingsUpdate
): Promise<void> {
  const res = await api.put<LangfuseUpdateResponse>(
    '/api/option/langfuse',
    payload
  )
  if (!res.data.success) {
    throw new Error(res.data.message || 'Failed to update Langfuse settings')
  }
}
