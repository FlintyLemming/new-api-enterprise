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
import * as z from 'zod'

import type { LangfuseSettingsView } from './langfuse-api'

// These rules mirror setting/langfuse_setting.Validate. The backend stays the
// authority — it rejects the whole group atomically — but repeating the rules
// here turns a round trip into an inline field error, and keeps the reachable
// bounds (each maximum is reachable with the other fields at their minimum)
// visible to the administrator.

export const LANGFUSE_BOUNDS = {
  contentBytes: { min: 4096, max: 1048576 },
  responseBytes: { min: 65536, max: 8388608 },
  sessionBodyBytes: { min: 1024, max: 65536 },
  queueSize: { min: 16, max: 256 },
  batchSize: { min: 1, max: 32 },
  flushIntervalSeconds: { min: 1, max: 300 },
  /** Headroom below the single-span warning Langfuse emits on ingestion. */
  maxQueuedSpanBytes: 9_000_000,
  /** Local planning ceiling for queued, in-flight and encoded span bodies. */
  queueBodyPlanningBytes: 256 * 1024 * 1024,
} as const

/** Presets only ever add one exact path; none of them is selected by default. */
export const LANGFUSE_SESSION_PATH_PRESETS = [
  'metadata.session_id',
  'metadata.conversation_id',
  'conversation_id',
  'chat_id',
] as const

export const LANGFUSE_VALIDATION_MESSAGES = {
  hostAbsolute: 'Host must be an absolute http or https URL',
  hostNoCredentials: 'Host cannot contain user info, a query or a fragment',
  hostNoTracesPath:
    'Host must be the Langfuse base URL without the OTLP traces path',
  hostRequired: 'Enter the Langfuse host before enabling tracing',
  publicKeyRequired: 'Enter the public key before enabling Langfuse tracing',
  secretClearRequiresDisabled:
    'Disable Langfuse tracing before clearing the secret key',
  environmentPattern:
    'Environment accepts up to 40 lowercase letters, digits, hyphens and underscores',
  environmentReserved: 'Environment cannot start with langfuse',
  sampleRateRange: 'Sample rate must be between 0 and 1',
  contentRange: 'Content capture limit must be between 4096 and 1048576 bytes',
  responseRange:
    'Response capture limit must be between 65536 and 8388608 bytes',
  sessionBodyRange:
    'Session body read limit must be between 1024 and 65536 bytes',
  queueRange: 'Queue size must be between 16 and 256',
  batchRange: 'Batch size must be between 1 and 32',
  batchOverQueue: 'Batch size cannot exceed the queue size',
  flushRange: 'Flush interval must be between 1 and 300 seconds',
  budgetBelowReservation:
    'Global capture budget must be at least the per-request reservation',
  spanBodyLimit:
    'Two content limits plus one response limit cannot exceed 9000000 bytes',
  queuePlanningLimit:
    'Queue and batch planning cannot exceed 256 MiB of span bodies',
  headerNameInvalid: 'Enter valid HTTP header names, one per line',
  headerNameCredential: 'Credential headers cannot be used as a session source',
  bodyPathWhitespace: 'Session body paths cannot start or end with spaces',
} as const

export const LANGFUSE_ENABLE_ERRORS = {
  sampleRate: 'Choose a sample rate before enabling Langfuse tracing',
  sendContent:
    'Choose whether to send prompts and responses before enabling Langfuse tracing',
  secretKey: 'Enter the secret key before enabling Langfuse tracing',
} as const

const environmentPattern = /^[a-z0-9_-]{1,40}$/
// RFC 7230 token characters, the only ones allowed in an HTTP field name.
const httpTokenPattern = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/
const credentialHeaders = new Set([
  'authorization',
  'cookie',
  'proxy-authorization',
  'set-cookie',
])
const tracesPath = '/api/public/otel/v1/traces'

/**
 * Splits a textarea into configured entries. Blank lines are dropped, but the
 * entries themselves are left untouched so surrounding whitespace surfaces as a
 * validation error instead of being silently rewritten.
 */
export function splitConfiguredLines(value: string): string[] {
  return value.split(/\r?\n/).filter((line) => line.trim() !== '')
}

function hostIssue(raw: string): string | null {
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return LANGFUSE_VALIDATION_MESSAGES.hostAbsolute
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return LANGFUSE_VALIDATION_MESSAGES.hostAbsolute
  }
  if (!parsed.host) return LANGFUSE_VALIDATION_MESSAGES.hostAbsolute
  if (parsed.username || parsed.password || parsed.search || parsed.hash) {
    return LANGFUSE_VALIDATION_MESSAGES.hostNoCredentials
  }
  const basePath = parsed.pathname.replace(/\/+$/, '')
  if (basePath.endsWith(tracesPath)) {
    return LANGFUSE_VALIDATION_MESSAGES.hostNoTracesPath
  }
  return null
}

const langfuseFormObject = z.object({
  enabled: z.boolean(),
  host: z.string(),
  public_key: z.string(),
  secret_key: z.string(),
  secret_key_clear: z.boolean(),
  environment: z
    .string()
    .regex(environmentPattern, LANGFUSE_VALIDATION_MESSAGES.environmentPattern),
  sample_rate: z
    .number()
    .min(0, LANGFUSE_VALIDATION_MESSAGES.sampleRateRange)
    .max(1, LANGFUSE_VALIDATION_MESSAGES.sampleRateRange),
  send_content: z.boolean(),
  max_content_bytes: z
    .number()
    .int(LANGFUSE_VALIDATION_MESSAGES.contentRange)
    .min(
      LANGFUSE_BOUNDS.contentBytes.min,
      LANGFUSE_VALIDATION_MESSAGES.contentRange
    )
    .max(
      LANGFUSE_BOUNDS.contentBytes.max,
      LANGFUSE_VALIDATION_MESSAGES.contentRange
    ),
  max_response_bytes: z
    .number()
    .int(LANGFUSE_VALIDATION_MESSAGES.responseRange)
    .min(
      LANGFUSE_BOUNDS.responseBytes.min,
      LANGFUSE_VALIDATION_MESSAGES.responseRange
    )
    .max(
      LANGFUSE_BOUNDS.responseBytes.max,
      LANGFUSE_VALIDATION_MESSAGES.responseRange
    ),
  max_in_flight_capture_bytes: z
    .number()
    .int(LANGFUSE_VALIDATION_MESSAGES.budgetBelowReservation)
    .positive(LANGFUSE_VALIDATION_MESSAGES.budgetBelowReservation),
  max_session_body_bytes: z
    .number()
    .int(LANGFUSE_VALIDATION_MESSAGES.sessionBodyRange)
    .min(
      LANGFUSE_BOUNDS.sessionBodyBytes.min,
      LANGFUSE_VALIDATION_MESSAGES.sessionBodyRange
    )
    .max(
      LANGFUSE_BOUNDS.sessionBodyBytes.max,
      LANGFUSE_VALIDATION_MESSAGES.sessionBodyRange
    ),
  session_header_names: z.string(),
  session_body_paths: z.string(),
  queue_size: z
    .number()
    .int(LANGFUSE_VALIDATION_MESSAGES.queueRange)
    .min(LANGFUSE_BOUNDS.queueSize.min, LANGFUSE_VALIDATION_MESSAGES.queueRange)
    .max(
      LANGFUSE_BOUNDS.queueSize.max,
      LANGFUSE_VALIDATION_MESSAGES.queueRange
    ),
  batch_size: z
    .number()
    .int(LANGFUSE_VALIDATION_MESSAGES.batchRange)
    .min(LANGFUSE_BOUNDS.batchSize.min, LANGFUSE_VALIDATION_MESSAGES.batchRange)
    .max(
      LANGFUSE_BOUNDS.batchSize.max,
      LANGFUSE_VALIDATION_MESSAGES.batchRange
    ),
  flush_interval_seconds: z
    .number()
    .int(LANGFUSE_VALIDATION_MESSAGES.flushRange)
    .min(
      LANGFUSE_BOUNDS.flushIntervalSeconds.min,
      LANGFUSE_VALIDATION_MESSAGES.flushRange
    )
    .max(
      LANGFUSE_BOUNDS.flushIntervalSeconds.max,
      LANGFUSE_VALIDATION_MESSAGES.flushRange
    ),
})

export const langfuseFormSchema = langfuseFormObject.superRefine(
  (values, ctx) => {
    if (values.host.trim()) {
      const issue = hostIssue(values.host.trim())
      if (issue) {
        ctx.addIssue({ code: 'custom', path: ['host'], message: issue })
      }
    }
    if (values.environment.startsWith('langfuse')) {
      ctx.addIssue({
        code: 'custom',
        path: ['environment'],
        message: LANGFUSE_VALIDATION_MESSAGES.environmentReserved,
      })
    }
    if (values.batch_size > values.queue_size) {
      ctx.addIssue({
        code: 'custom',
        path: ['batch_size'],
        message: LANGFUSE_VALIDATION_MESSAGES.batchOverQueue,
      })
    }

    // One capture reserves two content buffers plus one response buffer; the
    // same expression bounds a single exported span body.
    const reservation = 2 * values.max_content_bytes + values.max_response_bytes
    if (reservation > LANGFUSE_BOUNDS.maxQueuedSpanBytes) {
      ctx.addIssue({
        code: 'custom',
        path: ['max_response_bytes'],
        message: LANGFUSE_VALIDATION_MESSAGES.spanBodyLimit,
      })
    }
    if (values.max_in_flight_capture_bytes < reservation) {
      ctx.addIssue({
        code: 'custom',
        path: ['max_in_flight_capture_bytes'],
        message: LANGFUSE_VALIDATION_MESSAGES.budgetBelowReservation,
      })
    }
    const plannedSlots = values.queue_size + 3 * values.batch_size
    if (plannedSlots * reservation > LANGFUSE_BOUNDS.queueBodyPlanningBytes) {
      ctx.addIssue({
        code: 'custom',
        path: ['queue_size'],
        message: LANGFUSE_VALIDATION_MESSAGES.queuePlanningLimit,
      })
    }

    for (const name of splitConfiguredLines(values.session_header_names)) {
      if (!httpTokenPattern.test(name)) {
        ctx.addIssue({
          code: 'custom',
          path: ['session_header_names'],
          message: LANGFUSE_VALIDATION_MESSAGES.headerNameInvalid,
        })
        break
      }
      if (credentialHeaders.has(name.toLowerCase())) {
        ctx.addIssue({
          code: 'custom',
          path: ['session_header_names'],
          message: LANGFUSE_VALIDATION_MESSAGES.headerNameCredential,
        })
        break
      }
    }
    for (const bodyPath of splitConfiguredLines(values.session_body_paths)) {
      if (bodyPath !== bodyPath.trim()) {
        ctx.addIssue({
          code: 'custom',
          path: ['session_body_paths'],
          message: LANGFUSE_VALIDATION_MESSAGES.bodyPathWhitespace,
        })
        break
      }
    }

    // Clearing the secret is only offered once tracing is off, because a live
    // exporter would start failing authentication mid-flight.
    if (values.secret_key_clear && values.enabled) {
      ctx.addIssue({
        code: 'custom',
        path: ['secret_key_clear'],
        message: LANGFUSE_VALIDATION_MESSAGES.secretClearRequiresDisabled,
      })
    }
    if (!values.enabled) return
    if (!values.host.trim()) {
      ctx.addIssue({
        code: 'custom',
        path: ['host'],
        message: LANGFUSE_VALIDATION_MESSAGES.hostRequired,
      })
    }
    if (!values.public_key.trim()) {
      ctx.addIssue({
        code: 'custom',
        path: ['public_key'],
        message: LANGFUSE_VALIDATION_MESSAGES.publicKeyRequired,
      })
    }
  }
)

export type LangfuseFormValues = z.output<typeof langfuseFormSchema>

/** UI-only bookkeeping: the two decisions the enable step forces. */
export type LangfuseEnableConfirmation = LangfuseFormValues & {
  sample_rate_confirmed: boolean
  send_content_confirmed: boolean
}

/**
 * Turning tracing on always needs a fresh privacy and capacity decision, also
 * after it was switched off and back on — the backend refuses an enable that
 * omits sample_rate or send_content, and this keeps the UI from sending an
 * unconfirmed one. A stored secret stays sufficient; a missing one must be
 * typed before the exporter can authenticate.
 */
export function validateEnableTransition(
  persisted: LangfuseSettingsView,
  next: LangfuseEnableConfirmation
): string | null {
  if (!next.enabled) return null
  if (!persisted.enabled) {
    if (!next.sample_rate_confirmed) return LANGFUSE_ENABLE_ERRORS.sampleRate
    if (!next.send_content_confirmed) return LANGFUSE_ENABLE_ERRORS.sendContent
  }
  if (!persisted.secret_key_configured && !next.secret_key.trim()) {
    return LANGFUSE_ENABLE_ERRORS.secretKey
  }
  return null
}
