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
import assert from 'node:assert/strict'
import { describe, test } from 'node:test'

import {
  describeCaptureCapacity,
  formatCaptureBytes,
} from '../langfuse-capacity'

const defaultLimits = {
  max_content_bytes: 65536,
  max_response_bytes: 524288,
  max_in_flight_capture_bytes: 536870912,
  queue_size: 64,
  batch_size: 16,
}

describe('Langfuse capture capacity estimate', () => {
  test('reports the documented planning values for the shipped defaults', () => {
    const capacity = describeCaptureCapacity(defaultLimits)

    assert.deepEqual(capacity, {
      reservationBytes: 655360,
      captureSlots: 819,
      sampledRps: 27.3,
      residentBytes: 578813952,
      exportBytes: 610271232,
    })
  })

  test('follows a raised response limit into fewer concurrent slots', () => {
    const capacity = describeCaptureCapacity({
      ...defaultLimits,
      max_response_bytes: 1048576,
    })

    assert.equal(capacity.reservationBytes, 1179648)
    assert.equal(capacity.captureSlots, 455)
    assert.equal(capacity.sampledRps, 15.2)
  })

  test('reports no capture slots when the budget cannot hold one request', () => {
    const capacity = describeCaptureCapacity({
      ...defaultLimits,
      max_in_flight_capture_bytes: 1024,
    })

    assert.equal(capacity.captureSlots, 0)
    assert.equal(capacity.sampledRps, 0)
  })

  test('formats the reservation in KiB and the planning totals in MiB', () => {
    assert.equal(formatCaptureBytes(655360), '640 KiB')
    assert.equal(formatCaptureBytes(578813952), '552 MiB')
    assert.equal(formatCaptureBytes(610271232), '582 MiB')
  })
})
