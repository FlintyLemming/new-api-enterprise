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
import assert from 'node:assert/strict'
import { afterEach, describe, test } from 'node:test'

import { api } from '@/lib/api'

import {
  fetchLangfuseSettings,
  updateLangfuseSettings,
  type LangfuseSettingsUpdate,
  type LangfuseSettingsView,
} from '../langfuse-api'

type RequestMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: RequestMethod; put: RequestMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put

const persistedView: LangfuseSettingsView = {
  enabled: true,
  host: 'https://langfuse.example.com',
  public_key: 'pk-lf-1',
  secret_key_configured: true,
  environment: 'default',
  sample_rate: 0.1,
  send_content: false,
  max_content_bytes: 65536,
  max_response_bytes: 524288,
  max_in_flight_capture_bytes: 536870912,
  max_session_body_bytes: 65536,
  session_header_names: ['X-Session-Id'],
  session_body_paths: ['metadata.session_id'],
  queue_size: 64,
  batch_size: 16,
  flush_interval_seconds: 5,
}

const updatePayload: LangfuseSettingsUpdate = {
  enabled: true,
  host: 'https://langfuse.example.com',
  public_key: 'pk-lf-1',
  secret_key: '',
  secret_key_clear: false,
  environment: 'default',
  sample_rate: 0.1,
  send_content: false,
  max_content_bytes: 65536,
  max_response_bytes: 524288,
  max_in_flight_capture_bytes: 536870912,
  max_session_body_bytes: 65536,
  session_header_names: ['X-Session-Id'],
  session_body_paths: ['metadata.session_id'],
  queue_size: 64,
  batch_size: 16,
  flush_interval_seconds: 5,
}

afterEach(() => {
  apiClient.get = originalGet
  apiClient.put = originalPut
})

describe('Langfuse settings API client', () => {
  test('reads the dedicated endpoint and unwraps the settings view', async () => {
    const requestedUrls: string[] = []
    apiClient.get = async (url) => {
      requestedUrls.push(url)
      return { data: { success: true, message: '', data: persistedView } }
    }

    const view = await fetchLangfuseSettings()

    assert.deepEqual(requestedUrls, ['/api/option/langfuse'])
    assert.deepEqual(view, persistedView)
  })

  test('rejects with the server message when reading fails', async () => {
    apiClient.get = async () => ({
      data: { success: false, message: 'no permission' },
    })

    await assert.rejects(fetchLangfuseSettings(), /no permission/)
  })

  test('sends the whole configuration group to the dedicated endpoint', async () => {
    const requests: Array<{ url: string; body: unknown }> = []
    apiClient.put = async (url, data) => {
      requests.push({ url, body: data })
      return { data: { success: true, message: '' } }
    }

    await updateLangfuseSettings(updatePayload)

    assert.equal(requests.length, 1)
    assert.equal(requests[0]?.url, '/api/option/langfuse')
    assert.deepEqual(requests[0]?.body, updatePayload)
  })

  test('rejects with the server message when the update is refused', async () => {
    apiClient.put = async () => ({
      data: { success: false, message: 'host 必须是绝对 http/https URL' },
    })

    await assert.rejects(
      updateLangfuseSettings(updatePayload),
      /host 必须是绝对 http\/https URL/
    )
  })
})
