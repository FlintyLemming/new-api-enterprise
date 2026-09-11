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
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { Toaster, toast } = await import('sonner')
const { api } = await import('@/lib/api')
const { ResetCardUseButton } = await import('../reset-card-use-button')

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

function renderButton(onUsed = () => undefined) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <ResetCardUseButton subscriptionId={9201} onUsed={onUsed} />
        <Toaster duration={60_000} />
      </I18nextProvider>
    </QueryClientProvider>
  )
}

describe('reset card use button', () => {
  test('shows remaining count and stays enabled when cards are available', async () => {
    apiClient.get = async () => ({
      data: { success: true, data: { count: 2 } },
    })
    renderButton()
    const button = await screen.findByRole('button', {
      name: /use reset card.*2/i,
    })
    expect(button).toBeEnabled()
  })

  test('disables the button when no cards are available', async () => {
    apiClient.get = async () => ({
      data: { success: true, data: { count: 0 } },
    })
    renderButton()
    const button = await screen.findByRole('button', {
      name: /use reset card/i,
    })
    expect(button).toBeDisabled()
  })

  test('confirming the dialog calls the use API and refreshes via onUsed', async () => {
    const postCalls: { url: string; data?: unknown }[] = []
    let count = 1
    apiClient.get = async () => ({ data: { success: true, data: { count } } })
    apiClient.post = async (url: string, data?: unknown) => {
      postCalls.push({ url, data })
      count = 0
      return {
        data: { success: true, data: { card_id: 5, subscription_id: 9201 } },
      }
    }
    const onUsed = vi.fn()
    renderButton(onUsed)

    const button = await screen.findByRole('button', {
      name: /use reset card \(1 left\)/i,
    })
    fireEvent.click(button)

    // 确认框出现，说明文案解释清零语义
    expect(await screen.findByText(/clear the used quota/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^use reset card$/i }))

    await waitFor(() => {
      expect(postCalls).toHaveLength(1)
    })
    expect(postCalls[0].url).toBe('/api/subscription/self/reset_cards/use')
    expect(postCalls[0].data).toEqual({ subscription_id: 9201 })
    await waitFor(() => {
      expect(onUsed).toHaveBeenCalled()
    })
  })
})
