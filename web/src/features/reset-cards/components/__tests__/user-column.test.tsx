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
import {
  flexRender,
  getCoreRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { cleanup, render, screen, within } from '@testing-library/react'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, it } from 'vitest'

import type { ResetCard } from '../../types'
import { useResetCardsColumns } from '../reset-cards-columns'

const i18n = createInstance()
await i18n.init({
  lng: 'en',
  resources: { en: { translation: {} } },
  initAsync: false,
})

function makeCard(overrides: Partial<ResetCard>): ResetCard {
  return {
    id: 1,
    name: '补偿卡',
    user_id: 42,
    status: 1,
    created_time: 1700000000,
    used_time: 0,
    expired_time: 0,
    used_subscription_id: 0,
    ...overrides,
  }
}

function UserColumnTable(props: { card: ResetCard }) {
  const columns = useResetCardsColumns().filter(
    (column) => 'accessorKey' in column && column.accessorKey === 'user_id'
  )
  const table = useReactTable({
    columns,
    data: [props.card],
    getCoreRowModel: getCoreRowModel(),
  })
  return (
    <table>
      <thead>
        {table.getHeaderGroups().map((group) => (
          <tr key={group.id}>
            {group.headers.map((header) => (
              <th key={header.id}>
                {flexRender(
                  header.column.columnDef.header,
                  header.getContext()
                )}
              </th>
            ))}
          </tr>
        ))}
      </thead>
      <tbody>
        {table.getRowModel().rows.map((row) => (
          <tr key={row.id}>
            {row.getVisibleCells().map((cell) => (
              <td key={cell.id}>
                {flexRender(cell.column.columnDef.cell, cell.getContext())}
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

afterEach(() => {
  cleanup()
})

it('shows the card owner username above the user ID when the API returns one', () => {
  render(
    <I18nextProvider i18n={i18n}>
      <UserColumnTable card={makeCard({ user_id: 42, username: 'alice' })} />
    </I18nextProvider>
  )

  expect(
    screen.getByRole('columnheader', { name: 'User' })
  ).toBeInTheDocument()
  const cell = screen.getByRole('cell')
  expect(within(cell).getByText('alice')).toBeInTheDocument()
  expect(within(cell).getByText('User ID: 42')).toHaveAttribute(
    'data-table-text',
    'secondary'
  )
})

it('falls back to the bare user ID when the owner has no username', () => {
  render(
    <I18nextProvider i18n={i18n}>
      <UserColumnTable card={makeCard({ user_id: 42, username: undefined })} />
    </I18nextProvider>
  )

  const cell = screen.getByRole('cell')
  expect(within(cell).getByText('42')).toBeInTheDocument()
  expect(within(cell).queryByText('User ID: 42')).not.toBeInTheDocument()
})
