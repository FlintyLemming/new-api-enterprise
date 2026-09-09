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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { PricingSection } from '../pricing-section'

// FormNavigationGuard 依赖 TanStack Router 上下文，与本次测试的行为无关。
vi.mock('../../components/form-navigation-guard', () => ({
  FormNavigationGuard: () => null,
}))

const defaultValues = {
  QuotaPerUnit: 500000,
  USDExchangeRate: 7,
  DisplayInCurrencyEnabled: true,
  DisplayTokenStatEnabled: true,
  general_setting: {
    quota_display_type: 'USD' as const,
    custom_currency_symbol: '¤',
    custom_currency_exchange_rate: 1,
    usage_stats_cache_caliber: 'upstream' as const,
  },
}

function renderSection() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  queryClient.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  const actionsContainer = document.createElement('div')
  document.body.appendChild(actionsContainer)
  render(
    <QueryClientProvider client={queryClient}>
      <SettingsPageProvider actionsContainer={actionsContainer}>
        <PricingSection defaultValues={defaultValues} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
}

afterEach(cleanup)

describe('usage stats cache caliber setting', () => {
  test('renders the caliber select with the upstream default', () => {
    renderSection()
    const trigger = screen.getByRole('combobox', {
      name: 'Usage Stats Cache Caliber',
    })
    expect(trigger).toHaveTextContent('Upstream original')
  })

  test('submits the selected caliber as a system option', async () => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true, message: '' } })
    renderSection()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('combobox', { name: 'Usage Stats Cache Caliber' })
    )
    await user.click(
      await screen.findByRole('option', { name: 'Exclude cache tokens' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/option/', {
        key: 'general_setting.usage_stats_cache_caliber',
        value: 'exclude_cache',
      })
    )
  })
})
