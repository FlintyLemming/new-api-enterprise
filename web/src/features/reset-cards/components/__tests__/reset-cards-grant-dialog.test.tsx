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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

const i18n = (await import('i18next')).default
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { Toaster, toast } = await import('sonner')
const { api } = await import('@/lib/api')
const { ResetCardsGrantDialog } = await import('../reset-cards-grant-dialog')

await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
  toast.dismiss()
})

function renderDialog(
  onOpenChange = () => undefined,
  onSuccess = () => undefined
) {
  render(
    <I18nextProvider i18n={i18n}>
      <ResetCardsGrantDialog
        open
        onOpenChange={onOpenChange}
        onSuccess={onSuccess}
      />
      <Toaster duration={60_000} />
    </I18nextProvider>
  )
}

describe('reset card grant dialog', () => {
  test('rejects count below 1 and above 100 without calling the API', async () => {
    const postCalls: unknown[] = []
    apiClient.get = async () => ({
      data: { success: true, data: { items: [], total: 0 } },
    })
    apiClient.post = async (url: string, data?: unknown) => {
      postCalls.push({ url, data })
      return { data: { success: true, data: null } }
    }
    renderDialog()

    fireEvent.change(screen.getByLabelText(/name/i), {
      target: { value: '补偿卡' },
    })
    const countInput = screen.getByLabelText(/count/i)

    fireEvent.change(countInput, { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: /grant/i }))
    expect(
      await screen.findByText('Count must be between 1 and 100')
    ).toBeInTheDocument()

    fireEvent.change(countInput, { target: { value: '101' } })
    fireEvent.click(screen.getByRole('button', { name: /grant/i }))
    expect(
      await screen.findAllByText('Count must be between 1 and 100')
    ).not.toHaveLength(0)

    expect(postCalls).toHaveLength(0)
  })

  test('submits a valid grant, closes the dialog and calls onSuccess', async () => {
    const postCalls: { url: string; data?: unknown }[] = []
    apiClient.get = async (url: string) => {
      if (url.startsWith('/api/user/search')) {
        return {
          data: {
            success: true,
            data: {
              items: [{ id: 7, username: 'alice', display_name: 'Alice' }],
              total: 1,
              page: 1,
              page_size: 10,
            },
          },
        }
      }
      return { data: { success: false } }
    }
    apiClient.post = async (url: string, data?: unknown) => {
      postCalls.push({ url, data })
      return { data: { success: true, data: null } }
    }

    const onOpenChange = vi.fn()
    const onSuccess = vi.fn()
    renderDialog(onOpenChange, onSuccess)

    // 搜索并选择用户
    fireEvent.change(screen.getByPlaceholderText(/search user/i), {
      target: { value: 'alice' },
    })
    const userOption = await screen.findByRole('button', { name: /alice/i })
    fireEvent.click(userOption)

    fireEvent.change(screen.getByLabelText(/name/i), {
      target: { value: '9月补偿卡' },
    })
    fireEvent.change(screen.getByLabelText(/count/i), {
      target: { value: '2' },
    })
    fireEvent.click(screen.getByRole('button', { name: /grant/i }))

    await waitFor(() => {
      expect(postCalls).toHaveLength(1)
    })
    expect(postCalls[0].url).toBe('/api/subscription/admin/reset_cards/grant')
    expect(postCalls[0].data).toMatchObject({
      user_id: 7,
      name: '9月补偿卡',
      count: 2,
      expired_time: 0,
    })
    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false)
      expect(onSuccess).toHaveBeenCalled()
    })
  })
})
