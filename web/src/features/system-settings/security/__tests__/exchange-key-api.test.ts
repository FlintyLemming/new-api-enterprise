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
import { afterEach, describe, test } from 'vitest'

import { api } from '@/lib/api'

import {
  fetchExchangeKeySettings,
  updateExchangeKeySettings,
  type ExchangeKeySettingsUpdate,
  type ExchangeKeySettingsView,
} from '../exchange-key-api'

type RequestMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: RequestMethod; put: RequestMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put

const persistedView: ExchangeKeySettingsView = {
  enabled: true,
  secret_configured: true,
  secret_from_env: false,
  enabled_from_env: false,
}

const updatePayload: ExchangeKeySettingsUpdate = {
  enabled: true,
  secret_key: '',
  secret_key_clear: false,
}

afterEach(() => {
  apiClient.get = originalGet
  apiClient.put = originalPut
})

describe('Exchange Key settings API client', () => {
  test('reads the dedicated endpoint and unwraps the settings view', async () => {
    const requestedUrls: string[] = []
    apiClient.get = async (url) => {
      requestedUrls.push(url)
      return { data: { success: true, message: '', data: persistedView } }
    }

    const view = await fetchExchangeKeySettings()

    assert.deepEqual(requestedUrls, ['/api/option/exchange-key'])
    assert.deepEqual(view, persistedView)
  })

  test('rejects with the server message when reading fails', async () => {
    apiClient.get = async () => ({
      data: { success: false, message: 'no permission' },
    })

    await assert.rejects(fetchExchangeKeySettings(), /no permission/)
  })

  test('sends the configuration to the dedicated endpoint', async () => {
    const requests: Array<{ url: string; body: unknown }> = []
    apiClient.put = async (url, data) => {
      requests.push({ url, body: data })
      return { data: { success: true, message: '' } }
    }

    await updateExchangeKeySettings(updatePayload)

    assert.equal(requests.length, 1)
    assert.equal(requests[0]?.url, '/api/option/exchange-key')
    assert.deepEqual(requests[0]?.body, updatePayload)
  })

  test('rejects with the server message when the update is refused', async () => {
    apiClient.put = async () => ({
      data: { success: false, message: 'SECRET 长度不能少于 16 个字符' },
    })

    await assert.rejects(
      updateExchangeKeySettings(updatePayload),
      /SECRET 长度不能少于 16 个字符/
    )
  })
})
