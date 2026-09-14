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
import { z } from 'zod'

export const resetCardSchema = z.object({
  id: z.number(),
  name: z.string(),
  user_id: z.number(),
  /** 持卡人用户名，由后端列表接口填充；用户已删除时可能缺失 */
  username: z.string().optional(),
  status: z.number(),
  created_time: z.number(),
  used_time: z.number(),
  expired_time: z.number(),
  used_subscription_id: z.number(),
})

export type ResetCard = z.infer<typeof resetCardSchema>

/** Generic API response */
export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

export interface GetResetCardsParams {
  p?: number
  page_size?: number
}

export interface GetResetCardsResponse {
  success: boolean
  message?: string
  data?: {
    items: ResetCard[]
    total: number
    page: number
    page_size: number
  }
}

export interface SearchResetCardsParams extends GetResetCardsParams {
  keyword?: string
  status?: string
}

export interface GrantResetCardsPayload {
  user_id: number
  name: string
  count: number
  expired_time: number // 秒级 Unix 时间戳，0 = 不过期
}

export type ResetCardsDialogType = 'grant' | 'disable' | 'delete'
