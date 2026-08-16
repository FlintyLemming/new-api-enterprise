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

// Planning arithmetic behind the capacity hint. A capture reserves its full
// budget up front — two content buffers plus one response buffer — so the
// number of concurrent captures is a division, not a function of the response
// actually produced. These are estimates for sizing the limits; they neither
// bill the real response size nor replace whole-process memory planning.

const KIBIBYTE = 1024
const MEBIBYTE = 1024 * 1024

/** Reference capture lifetime used to turn slots into a throughput estimate. */
export const LANGFUSE_AVERAGE_CAPTURE_SECONDS = 30

export type LangfuseCaptureLimits = {
  max_content_bytes: number
  max_response_bytes: number
  max_in_flight_capture_bytes: number
  queue_size: number
  batch_size: number
}

export type LangfuseCaptureCapacity = {
  /** Bytes a single sampled request reserves from the global budget. */
  reservationBytes: number
  /**
   * Bytes one queued span body can reach: one input plus one output, both
   * reduced to the content limit. The response buffer is aggregated away before
   * a span is queued, so it is deliberately absent here.
   */
  spanBodyBytes: number
  /** Concurrent sampled requests the budget admits. */
  captureSlots: number
  sampledRps: number
  /** Budget plus a full export queue of span bodies. */
  residentBytes: number
  /** Resident planning value plus the batches in flight to Langfuse. */
  exportBytes: number
}

function roundToTenth(value: number): number {
  return Number(value.toFixed(1))
}

export function describeCaptureCapacity(
  limits: LangfuseCaptureLimits
): LangfuseCaptureCapacity {
  const spanBodyBytes = 2 * limits.max_content_bytes
  const reservationBytes = spanBodyBytes + limits.max_response_bytes
  const captureSlots =
    reservationBytes > 0
      ? Math.floor(limits.max_in_flight_capture_bytes / reservationBytes)
      : 0

  return {
    reservationBytes,
    spanBodyBytes,
    captureSlots,
    sampledRps: roundToTenth(captureSlots / LANGFUSE_AVERAGE_CAPTURE_SECONDS),
    residentBytes:
      limits.max_in_flight_capture_bytes + limits.queue_size * spanBodyBytes,
    exportBytes:
      limits.max_in_flight_capture_bytes +
      (limits.queue_size + 3 * limits.batch_size) * spanBodyBytes,
  }
}

export function formatCaptureBytes(bytes: number): string {
  if (bytes >= MEBIBYTE) return `${roundToTenth(bytes / MEBIBYTE)} MiB`
  if (bytes >= KIBIBYTE) return `${roundToTenth(bytes / KIBIBYTE)} KiB`
  return `${bytes} B`
}
