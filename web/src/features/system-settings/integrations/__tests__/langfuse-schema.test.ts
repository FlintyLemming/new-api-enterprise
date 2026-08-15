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

import type { LangfuseSettingsView } from '../langfuse-api'
import {
  LANGFUSE_ENABLE_ERRORS,
  LANGFUSE_VALIDATION_MESSAGES,
  langfuseFormSchema,
  splitConfiguredLines,
  validateEnableTransition,
  type LangfuseFormValues,
} from '../langfuse-schema'

// The disabled default configuration the backend ships with; every case below
// only lists what it changes so the table stays readable.
const baseValues: LangfuseFormValues = {
  enabled: false,
  host: '',
  public_key: '',
  secret_key: '',
  secret_key_clear: false,
  environment: 'default',
  sample_rate: 0.1,
  send_content: false,
  max_content_bytes: 65536,
  max_response_bytes: 524288,
  max_in_flight_capture_bytes: 536870912,
  max_session_body_bytes: 65536,
  session_header_names: '',
  session_body_paths: '',
  queue_size: 64,
  batch_size: 16,
  flush_interval_seconds: 5,
}

const enabledValues: LangfuseFormValues = {
  ...baseValues,
  enabled: true,
  host: 'https://langfuse.example.com',
  public_key: 'pk-lf-1',
  secret_key: 'sk-lf-1',
}

type SchemaCase = {
  name: string
  overrides: Partial<LangfuseFormValues>
  expected: { path: string; message: string } | 'accepted'
}

const schemaCases: SchemaCase[] = [
  { name: 'the shipped defaults', overrides: {}, expected: 'accepted' },
  {
    name: 'a fully configured enabled setup',
    overrides: enabledValues,
    expected: 'accepted',
  },
  {
    name: 'a host without a scheme',
    overrides: { host: 'langfuse.example.com' },
    expected: {
      path: 'host',
      message: LANGFUSE_VALIDATION_MESSAGES.hostAbsolute,
    },
  },
  {
    name: 'a non-http scheme',
    overrides: { host: 'ftp://langfuse.example.com' },
    expected: {
      path: 'host',
      message: LANGFUSE_VALIDATION_MESSAGES.hostAbsolute,
    },
  },
  {
    name: 'a host carrying user info',
    overrides: { host: 'https://user:pass@langfuse.example.com' },
    expected: {
      path: 'host',
      message: LANGFUSE_VALIDATION_MESSAGES.hostNoCredentials,
    },
  },
  {
    name: 'a host carrying a query string',
    overrides: { host: 'https://langfuse.example.com?token=1' },
    expected: {
      path: 'host',
      message: LANGFUSE_VALIDATION_MESSAGES.hostNoCredentials,
    },
  },
  {
    name: 'a host carrying a fragment',
    overrides: { host: 'https://langfuse.example.com#frag' },
    expected: {
      path: 'host',
      message: LANGFUSE_VALIDATION_MESSAGES.hostNoCredentials,
    },
  },
  {
    name: 'a host that already ends with the OTLP traces path',
    overrides: {
      host: 'https://langfuse.example.com/api/public/otel/v1/traces',
    },
    expected: {
      path: 'host',
      message: LANGFUSE_VALIDATION_MESSAGES.hostNoTracesPath,
    },
  },
  {
    name: 'a host with a base path and a trailing slash',
    overrides: { host: 'https://x.example/langfuse/' },
    expected: 'accepted',
  },
  {
    name: 'an IPv6 host with a port',
    overrides: { host: 'http://[::1]:3000' },
    expected: 'accepted',
  },
  {
    name: 'an enabled configuration without a host',
    overrides: { ...enabledValues, host: '' },
    expected: {
      path: 'host',
      message: LANGFUSE_VALIDATION_MESSAGES.hostRequired,
    },
  },
  {
    name: 'an enabled configuration without a public key',
    overrides: { ...enabledValues, public_key: '' },
    expected: {
      path: 'public_key',
      message: LANGFUSE_VALIDATION_MESSAGES.publicKeyRequired,
    },
  },
  {
    name: 'clearing the secret while tracing stays enabled',
    overrides: { ...enabledValues, secret_key_clear: true },
    expected: {
      path: 'secret_key_clear',
      message: LANGFUSE_VALIDATION_MESSAGES.secretClearRequiresDisabled,
    },
  },
  {
    name: 'clearing the secret of a disabled configuration',
    overrides: { secret_key_clear: true },
    expected: 'accepted',
  },
  {
    name: 'an empty environment',
    overrides: { environment: '' },
    expected: {
      path: 'environment',
      message: LANGFUSE_VALIDATION_MESSAGES.environmentPattern,
    },
  },
  {
    name: 'an environment with uppercase letters',
    overrides: { environment: 'A_b' },
    expected: {
      path: 'environment',
      message: LANGFUSE_VALIDATION_MESSAGES.environmentPattern,
    },
  },
  {
    name: 'an environment longer than 40 characters',
    overrides: { environment: 'a'.repeat(41) },
    expected: {
      path: 'environment',
      message: LANGFUSE_VALIDATION_MESSAGES.environmentPattern,
    },
  },
  {
    name: 'an environment reserved by langfuse itself',
    overrides: { environment: 'langfuse-prod' },
    expected: {
      path: 'environment',
      message: LANGFUSE_VALIDATION_MESSAGES.environmentReserved,
    },
  },
  {
    name: 'a negative sample rate',
    overrides: { sample_rate: -0.1 },
    expected: {
      path: 'sample_rate',
      message: LANGFUSE_VALIDATION_MESSAGES.sampleRateRange,
    },
  },
  {
    name: 'a sample rate above one',
    overrides: { sample_rate: 1.1 },
    expected: {
      path: 'sample_rate',
      message: LANGFUSE_VALIDATION_MESSAGES.sampleRateRange,
    },
  },
  {
    name: 'a sample rate of exactly zero',
    overrides: { sample_rate: 0 },
    expected: 'accepted',
  },
  {
    name: 'a sample rate of exactly one',
    overrides: { sample_rate: 1 },
    expected: 'accepted',
  },
  {
    name: 'a content limit below the minimum',
    overrides: { max_content_bytes: 4095 },
    expected: {
      path: 'max_content_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.contentRange,
    },
  },
  {
    name: 'a content limit above the maximum',
    overrides: { max_content_bytes: 4194305 },
    expected: {
      path: 'max_content_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.contentRange,
    },
  },
  {
    name: 'a response limit below the minimum',
    overrides: { max_response_bytes: 65535 },
    expected: {
      path: 'max_response_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.responseRange,
    },
  },
  {
    name: 'a response limit above the maximum',
    overrides: { max_response_bytes: 8388609 },
    expected: {
      path: 'max_response_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.responseRange,
    },
  },
  {
    name: 'a session body limit below the minimum',
    overrides: { max_session_body_bytes: 1023 },
    expected: {
      path: 'max_session_body_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.sessionBodyRange,
    },
  },
  {
    name: 'a session body limit above the maximum',
    overrides: { max_session_body_bytes: 65537 },
    expected: {
      path: 'max_session_body_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.sessionBodyRange,
    },
  },
  {
    name: 'a queue below the minimum',
    overrides: { queue_size: 15 },
    expected: {
      path: 'queue_size',
      message: LANGFUSE_VALIDATION_MESSAGES.queueRange,
    },
  },
  {
    name: 'a queue above the maximum',
    overrides: { queue_size: 257 },
    expected: {
      path: 'queue_size',
      message: LANGFUSE_VALIDATION_MESSAGES.queueRange,
    },
  },
  {
    name: 'a batch of zero',
    overrides: { batch_size: 0 },
    expected: {
      path: 'batch_size',
      message: LANGFUSE_VALIDATION_MESSAGES.batchRange,
    },
  },
  {
    name: 'a batch above the maximum',
    overrides: { batch_size: 33 },
    expected: {
      path: 'batch_size',
      message: LANGFUSE_VALIDATION_MESSAGES.batchRange,
    },
  },
  {
    name: 'a batch larger than the queue',
    overrides: { queue_size: 16, batch_size: 32 },
    expected: {
      path: 'batch_size',
      message: LANGFUSE_VALIDATION_MESSAGES.batchOverQueue,
    },
  },
  {
    name: 'a flush interval of zero seconds',
    overrides: { flush_interval_seconds: 0 },
    expected: {
      path: 'flush_interval_seconds',
      message: LANGFUSE_VALIDATION_MESSAGES.flushRange,
    },
  },
  {
    name: 'a flush interval above 300 seconds',
    overrides: { flush_interval_seconds: 301 },
    expected: {
      path: 'flush_interval_seconds',
      message: LANGFUSE_VALIDATION_MESSAGES.flushRange,
    },
  },
  {
    // Each maximum stays reachable on its own with the other fields at their
    // minimum, so the UI never advertises a bound that cannot be saved.
    name: 'the content maximum with everything else at its minimum',
    overrides: {
      max_content_bytes: 4194304,
      max_response_bytes: 65536,
      queue_size: 16,
      batch_size: 1,
    },
    expected: 'accepted',
  },
  {
    // The audit-completeness configuration the deployment runs: a 4 MiB content
    // capture keeps long-context prompts whole. Reservation is
    // 2*4194304 + 524288 = 8,912,896 and planning (128 + 3*4) * 8,912,896 =
    // 1,247,805,440, both inside the raised ceilings.
    name: 'the 4 MiB audit content capture at a realistic queue and batch',
    overrides: {
      max_content_bytes: 4194304,
      max_response_bytes: 524288,
      max_in_flight_capture_bytes: 8589934592,
      queue_size: 128,
      batch_size: 4,
    },
    expected: 'accepted',
  },
  {
    name: 'the response maximum with everything else at its minimum',
    overrides: {
      max_content_bytes: 4096,
      max_response_bytes: 8388608,
      queue_size: 16,
      batch_size: 1,
    },
    expected: 'accepted',
  },
  {
    name: 'both content and response at their maximum',
    overrides: { max_content_bytes: 4194304, max_response_bytes: 8388608 },
    expected: {
      path: 'max_response_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.spanBodyLimit,
    },
  },
  {
    name: 'a capture budget one byte below the per-request reservation',
    overrides: { max_in_flight_capture_bytes: 655359 },
    expected: {
      path: 'max_in_flight_capture_bytes',
      message: LANGFUSE_VALIDATION_MESSAGES.budgetBelowReservation,
    },
  },
  {
    name: 'a capture budget exactly equal to the reservation',
    overrides: { max_in_flight_capture_bytes: 655360 },
    expected: 'accepted',
  },
  {
    // The span envelope stays legal at 8,912,896 bytes, but
    // (256 + 3*32) * 8,912,896 = 3,137,339,392 exceeds the planning ceiling.
    name: 'a queue and batch pair whose planned span bodies exceed 2 GiB',
    overrides: {
      max_content_bytes: 4194304,
      max_response_bytes: 524288,
      queue_size: 256,
      batch_size: 32,
    },
    expected: {
      path: 'queue_size',
      message: LANGFUSE_VALIDATION_MESSAGES.queuePlanningLimit,
    },
  },
  {
    name: 'a header name with a space',
    overrides: { session_header_names: 'X Session Id' },
    expected: {
      path: 'session_header_names',
      message: LANGFUSE_VALIDATION_MESSAGES.headerNameInvalid,
    },
  },
  {
    name: 'a header name with a colon',
    overrides: { session_header_names: 'X-Session:Id' },
    expected: {
      path: 'session_header_names',
      message: LANGFUSE_VALIDATION_MESSAGES.headerNameInvalid,
    },
  },
  {
    name: 'a credential header spelled in lowercase',
    overrides: { session_header_names: 'X-Session-Id\nauthorization' },
    expected: {
      path: 'session_header_names',
      message: LANGFUSE_VALIDATION_MESSAGES.headerNameCredential,
    },
  },
  {
    name: 'a cookie header as a session source',
    overrides: { session_header_names: 'Cookie' },
    expected: {
      path: 'session_header_names',
      message: LANGFUSE_VALIDATION_MESSAGES.headerNameCredential,
    },
  },
  {
    name: 'header names separated by blank lines',
    overrides: { session_header_names: 'X-Session-Id\n\nX-Conversation-Id\n' },
    expected: 'accepted',
  },
  {
    name: 'a body path padded with spaces',
    overrides: { session_body_paths: ' metadata.session_id' },
    expected: {
      path: 'session_body_paths',
      message: LANGFUSE_VALIDATION_MESSAGES.bodyPathWhitespace,
    },
  },
  {
    name: 'body paths one per line',
    overrides: { session_body_paths: 'metadata.session_id\nchat_id' },
    expected: 'accepted',
  },
]

describe('Langfuse settings form schema', () => {
  for (const schemaCase of schemaCases) {
    const verb = schemaCase.expected === 'accepted' ? 'accepts' : 'rejects'
    test(`${verb} ${schemaCase.name}`, () => {
      const result = langfuseFormSchema.safeParse({
        ...baseValues,
        ...schemaCase.overrides,
      })

      if (schemaCase.expected === 'accepted') {
        assert.equal(
          result.success,
          true,
          `expected acceptance, got ${JSON.stringify(result.error?.issues)}`
        )
        return
      }

      assert.equal(result.success, false)
      const issues = (result.error?.issues ?? []).map((issue) => ({
        path: issue.path.join('.'),
        message: issue.message,
      }))
      const expected = schemaCase.expected
      assert.ok(
        issues.some(
          (issue) =>
            issue.path === expected.path && issue.message === expected.message
        ),
        `expected ${JSON.stringify(expected)}, got ${JSON.stringify(issues)}`
      )
    })
  }
})

describe('Langfuse line-separated list parsing', () => {
  test('drops blank lines while preserving the configured entries verbatim', () => {
    assert.deepEqual(
      splitConfiguredLines('X-Session-Id\r\n\n   \nX-Chat-Id\n'),
      ['X-Session-Id', 'X-Chat-Id']
    )
  })

  test('returns an empty list for an untouched field', () => {
    assert.deepEqual(splitConfiguredLines('   \n\n'), [])
  })
})

const persistedDisabled: LangfuseSettingsView = {
  enabled: false,
  host: '',
  public_key: '',
  secret_key_configured: false,
  environment: 'default',
  sample_rate: 0.1,
  send_content: false,
  max_content_bytes: 65536,
  max_response_bytes: 524288,
  max_in_flight_capture_bytes: 536870912,
  max_session_body_bytes: 65536,
  session_header_names: [],
  session_body_paths: [],
  queue_size: 64,
  batch_size: 16,
  flush_interval_seconds: 5,
}

describe('Langfuse enable transition', () => {
  test('requires an explicit sample rate decision when switching tracing on', () => {
    const error = validateEnableTransition(persistedDisabled, {
      ...enabledValues,
      sample_rate_confirmed: false,
      send_content_confirmed: false,
    })

    assert.equal(error, LANGFUSE_ENABLE_ERRORS.sampleRate)
  })

  test('still requires the content decision once the sample rate is chosen', () => {
    const error = validateEnableTransition(persistedDisabled, {
      ...enabledValues,
      sample_rate_confirmed: true,
      send_content_confirmed: false,
    })

    assert.equal(error, LANGFUSE_ENABLE_ERRORS.sendContent)
  })

  test('accepts an explicit sample rate of zero as a paused capture', () => {
    const error = validateEnableTransition(persistedDisabled, {
      ...enabledValues,
      sample_rate: 0,
      sample_rate_confirmed: true,
      send_content_confirmed: true,
    })

    assert.equal(error, null)
  })

  test('asks for the secret key when none is stored yet', () => {
    const error = validateEnableTransition(persistedDisabled, {
      ...enabledValues,
      secret_key: '',
      sample_rate_confirmed: true,
      send_content_confirmed: true,
    })

    assert.equal(error, LANGFUSE_ENABLE_ERRORS.secretKey)
  })

  test('keeps the stored secret sufficient for an already enabled setup', () => {
    const error = validateEnableTransition(
      { ...persistedDisabled, enabled: true, secret_key_configured: true },
      {
        ...enabledValues,
        secret_key: '',
        sample_rate_confirmed: false,
        send_content_confirmed: false,
      }
    )

    assert.equal(error, null)
  })

  test('never asks for confirmation while tracing stays off', () => {
    const error = validateEnableTransition(persistedDisabled, {
      ...baseValues,
      sample_rate_confirmed: false,
      send_content_confirmed: false,
    })

    assert.equal(error, null)
  })
})
