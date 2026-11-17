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
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Toaster, toast } from 'sonner'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import type { SubscriptionPlan } from '../../types'
import { SubscriptionsDialogs } from '../subscriptions-dialogs'
import { SubscriptionsPrimaryButtons } from '../subscriptions-primary-buttons'
import { SubscriptionsProvider } from '../subscriptions-provider'

function plan(
  id: number,
  title: string,
  enabled: boolean
): { plan: SubscriptionPlan } {
  return {
    plan: {
      id,
      title,
      subtitle: '',
      price_amount: 0,
      currency: 'USD',
      duration_unit: 'year',
      duration_value: 100,
      custom_seconds: 0,
      quota_reset_period: 'custom',
      quota_reset_custom_seconds: 18000,
      enabled,
      sort_order: 0,
      allow_balance_pay: true,
      allow_wallet_overflow: true,
      max_purchase_per_user: 0,
      total_amount: 150000000,
      upgrade_group: '',
      downgrade_group: '',
      stripe_price_id: '',
      creem_product_id: '',
      waffo_pancake_product_id: '',
    },
  }
}

function complianceOptions(confirmed: boolean) {
  return [
    { key: 'payment_setting.compliance_confirmed', value: String(confirmed) },
    { key: 'payment_setting.compliance_terms_version', value: 'v1' },
  ]
}

function mockReads(
  savedPlanId: number,
  options: { complianceConfirmed?: boolean; settingsFail?: boolean } = {}
) {
  return vi.spyOn(api, 'get').mockImplementation(async (url: string) => {
    if (url === '/api/option/') {
      return {
        data: {
          success: true,
          message: '',
          data: complianceOptions(options.complianceConfirmed ?? true),
        },
      }
    }
    if (url === '/api/subscription/admin/settings' && options.settingsFail) {
      return { data: { success: false, message: 'Operation failed' } }
    }
    if (url === '/api/subscription/admin/plans') {
      return {
        data: {
          success: true,
          message: '',
          data: [plan(1, 'Starter', true), plan(2, '5h 150M', false)],
        },
      }
    }
    if (url === '/api/subscription/admin/settings') {
      return {
        data: {
          success: true,
          message: '',
          data: { new_user_plan_id: savedPlanId },
        },
      }
    }
    throw new Error(`unexpected GET ${url}`)
  })
}

afterEach(() => {
  toast.dismiss()
  vi.restoreAllMocks()
})

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <SubscriptionsProvider>
        <SubscriptionsPrimaryButtons />
        <SubscriptionsDialogs />
      </SubscriptionsProvider>
      <Toaster />
    </QueryClientProvider>
  )
}

async function openSettings() {
  const user = userEvent.setup()
  renderPage()
  const button = screen.getByRole('button', { name: 'Settings' })
  await waitFor(() => expect(button).toBeEnabled())
  await user.click(button)
  const dialog = await screen.findByRole('dialog')
  const select = within(dialog).getByRole('combobox', {
    name: 'New user default plan',
  })
  return { user, dialog, select }
}

describe('subscription settings dialog', () => {
  it('opening settings shows the saved default plan and marks disabled plans', async () => {
    mockReads(2)
    const { select } = await openSettings()

    await waitFor(() => expect(select).toHaveValue('2'))
    expect(
      within(select).getByRole('option', { name: '5h 150M (Disabled)' })
    ).toBeInTheDocument()
    expect(
      within(select).getByRole('option', { name: 'Starter' })
    ).toBeInTheDocument()
  })

  it('saving a selection sends the chosen plan id and closes the dialog', async () => {
    mockReads(2)
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: true, message: '', data: { new_user_plan_id: 1 } },
    })
    const { user, dialog, select } = await openSettings()
    await waitFor(() => expect(select).toHaveValue('2'))

    await user.selectOptions(select, '1')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/subscription/admin/settings', {
        new_user_plan_id: 1,
      })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
  })

  it('choosing do not grant saves zero', async () => {
    mockReads(2)
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: true, message: '', data: { new_user_plan_id: 0 } },
    })
    const { user, dialog, select } = await openSettings()
    await waitFor(() => expect(select).toHaveValue('2'))

    await user.selectOptions(select, '0')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/subscription/admin/settings', {
        new_user_plan_id: 0,
      })
    )
  })

  it('a failed save keeps the dialog open', async () => {
    mockReads(2)
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: false, message: 'Subscription plan does not exist' },
    })
    const { user, dialog, select } = await openSettings()
    await waitFor(() => expect(select).toHaveValue('2'))

    await user.selectOptions(select, '1')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))

    expect(
      await screen.findByText('Subscription plan does not exist')
    ).toBeInTheDocument()
    expect(put).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('a failed settings read disables saving so the setting is not overwritten', async () => {
    mockReads(2, { settingsFail: true })
    const put = vi.spyOn(api, 'put')
    const { dialog, select } = await openSettings()

    const save = within(dialog).getByRole('button', { name: 'Save' })
    await waitFor(() => expect(select).toBeDisabled())
    expect(save).toBeDisabled()
    expect(put).not.toHaveBeenCalled()
  })

  it('unconfirmed compliance terms disable the settings button', async () => {
    const get = mockReads(2, { complianceConfirmed: false })
    renderPage()

    const button = screen.getByRole('button', { name: 'Settings' })
    await waitFor(() => expect(get).toHaveBeenCalledWith('/api/option/'))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Create Plan' })).toBeDisabled()
    )
    expect(button).toBeDisabled()
  })
})
