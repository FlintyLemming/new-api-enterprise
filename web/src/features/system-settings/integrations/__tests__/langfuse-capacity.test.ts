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
      spanBodyBytes: 131072,
      captureSlots: 819,
      sampledRps: 27.3,
      residentBytes: 545259520,
      exportBytes: 551550976,
    })
  })

  // A streaming response buffer is framed SSE the exporter aggregates and
  // drops. Charging it to the queue planning made a buffer sized for a long
  // answer look like gigabytes of pending span bodies.
  test('keeps a raised response limit out of the queued span planning', () => {
    const capacity = describeCaptureCapacity({
      ...defaultLimits,
      max_content_bytes: 4194304,
      max_response_bytes: 67108864,
      max_in_flight_capture_bytes: 34359738368,
      queue_size: 128,
      batch_size: 4,
    })

    assert.equal(capacity.spanBodyBytes, 8388608)
    assert.equal(capacity.reservationBytes, 75497472)
    assert.equal(capacity.captureSlots, 455)
    // 34359738368 + (128 + 3*4) * 8388608, not the reservation.
    assert.equal(capacity.exportBytes, 35534143488)
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
    assert.equal(formatCaptureBytes(545259520), '520 MiB')
    assert.equal(formatCaptureBytes(551550976), '526 MiB')
  })
})
