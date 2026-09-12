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
import { render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import type { LogOtherData } from '../../types'
import { DetailsDialog } from '../dialogs/details-dialog'

const queryClients: QueryClient[] = []

function makeLog(other: LogOtherData, promptTokens = 850): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: 'user',
    token_name: 'token',
    model_name: 'gpt-test',
    quota: 1050,
    prompt_tokens: promptTokens,
    completion_tokens: 200,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: 'channel',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(isAdmin: boolean, other: LogOtherData): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(['status'], {}, { updatedAt: Date.now() + 60_000 })
  queryClients.push(queryClient)
  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog(other)}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const queryClient of queryClients) {
    queryClient.clear()
  }
  queryClients.length = 0
})

describe('usage log stats normalization', () => {
  test('shows original to normalized prompt tokens to admins', () => {
    renderDetails(true, {
      admin_info: {
        stats_normalization: {
          target: 'exclude_cache',
          upstream_caliber: 'include_cache',
          original_prompt_tokens: 1000,
          applied: true,
        },
      },
    })
    const sectionLabel = screen.getByText('Stats normalization')
    // The label sits in the section header; the section body holds the rows.
    const section = sectionLabel.parentElement
    expect(section).not.toBeNull()
    const rows = within(section as HTMLElement)
    expect(rows.getByText('1000')).toBeInTheDocument()
    expect(rows.getByText('850')).toBeInTheDocument()
  })

  test('shows the skip reason when normalization was not applied', () => {
    renderDetails(true, {
      admin_info: {
        stats_normalization: {
          target: 'exclude_cache',
          upstream_caliber: 'include_cache',
          original_prompt_tokens: 100,
          applied: false,
          skip_reason: 'prompt_less_than_cache',
        },
      },
    })
    expect(screen.getByText('prompt_less_than_cache')).toBeInTheDocument()
  })

  test('hides the normalization info from non-admin users', () => {
    renderDetails(false, {
      admin_info: {
        stats_normalization: {
          target: 'exclude_cache',
          upstream_caliber: 'include_cache',
          original_prompt_tokens: 1000,
          applied: true,
        },
      },
    })
    expect(screen.queryByText('Stats normalization')).toBeNull()
  })
})
