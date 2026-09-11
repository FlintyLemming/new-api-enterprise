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

import type {
  ApiResponse,
  GetResetCardsParams,
  GetResetCardsResponse,
  GrantResetCardsPayload,
  SearchResetCardsParams,
} from './types'

// ============================================================================
// Subscription Reset Card Management
// ============================================================================

// Get paginated reset cards list
export async function getResetCards(
  params: GetResetCardsParams = {}
): Promise<GetResetCardsResponse> {
  const { p = 1, page_size = 10 } = params
  const res = await api.get(
    `/api/subscription/admin/reset_cards/?p=${p}&page_size=${page_size}`
  )
  return res.data
}

// Search reset cards by keyword and status
export async function searchResetCards(
  params: SearchResetCardsParams
): Promise<GetResetCardsResponse> {
  const { keyword = '', status = '', p = 1, page_size = 10 } = params
  const queryParams = new URLSearchParams()
  queryParams.set('keyword', keyword)
  if (status) queryParams.set('status', status)
  queryParams.set('p', String(p))
  queryParams.set('page_size', String(page_size))
  const res = await api.get(
    `/api/subscription/admin/reset_cards/search?${queryParams.toString()}`
  )
  return res.data
}

// Grant reset cards to a user
export async function grantResetCards(
  payload: GrantResetCardsPayload
): Promise<ApiResponse<null>> {
  const res = await api.post('/api/subscription/admin/reset_cards/grant', payload)
  return res.data
}

// Disable reset cards by IDs
export async function disableResetCards(
  ids: number[]
): Promise<ApiResponse<number>> {
  const res = await api.post('/api/subscription/admin/reset_cards/disable', {
    ids,
  })
  return res.data
}

// Delete a single reset card
export async function deleteResetCard(id: number): Promise<ApiResponse<null>> {
  const res = await api.delete(`/api/subscription/admin/reset_cards/${id}`)
  return res.data
}
