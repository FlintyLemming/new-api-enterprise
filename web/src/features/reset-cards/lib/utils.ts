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
import { RESET_CARD_STATUS } from '../constants'

/** timestamp 为秒级 Unix 时间戳；0 表示不过期 */
export function isTimestampExpired(timestamp: number): boolean {
  return timestamp !== 0 && timestamp * 1000 < Date.now()
}

export function isResetCardExpired(
  expiredTime: number,
  status: number
): boolean {
  return status === RESET_CARD_STATUS.UNUSED && isTimestampExpired(expiredTime)
}
