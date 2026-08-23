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
const SECRET_CHARS =
  '0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ'

export const GENERATED_SECRET_LENGTH = 32

export function generateExchangeKeySecret(): string {
  const maxUnbiased =
    Math.floor(256 / SECRET_CHARS.length) * SECRET_CHARS.length
  const chars: string[] = []
  while (chars.length < GENERATED_SECRET_LENGTH) {
    const bytes = new Uint8Array(GENERATED_SECRET_LENGTH - chars.length)
    globalThis.crypto.getRandomValues(bytes)
    for (const byte of bytes) {
      if (byte >= maxUnbiased) {
        continue
      }
      chars.push(SECRET_CHARS[byte % SECRET_CHARS.length])
      if (chars.length === GENERATED_SECRET_LENGTH) {
        break
      }
    }
  }
  return chars.join('')
}
