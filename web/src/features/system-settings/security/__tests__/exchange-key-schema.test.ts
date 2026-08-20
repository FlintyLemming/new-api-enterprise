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

import type { ExchangeKeySettingsView } from '../exchange-key-api'
import {
  exchangeKeyFormSchema,
  type ExchangeKeyFormValues,
} from '../exchange-key-schema'

const storedView: ExchangeKeySettingsView = {
  enabled: true,
  secret_configured: true,
  secret_from_env: false,
  enabled_from_env: false,
}

const unconfiguredView: ExchangeKeySettingsView = {
  ...storedView,
  secret_configured: false,
}

const enabledKeepSecret: ExchangeKeyFormValues = {
  enabled: true,
  secret_key: '',
  secret_key_clear: false,
}

function firstIssue(
  values: ExchangeKeyFormValues,
  view: ExchangeKeySettingsView
) {
  const result = exchangeKeyFormSchema(view).safeParse(values)
  assert.equal(result.success, false)
  return result.error.issues[0]
}

describe('Exchange Key form schema', () => {
  test('accepts an empty secret when a stored secret is already configured', () => {
    const result =
      exchangeKeyFormSchema(storedView).safeParse(enabledKeepSecret)
    assert.equal(result.success, true)
  })

  test('rejects enabling without a secret when none is configured', () => {
    const issue = firstIssue(enabledKeepSecret, unconfiguredView)
    assert.equal(issue?.path[0], 'secret_key')
    assert.equal(issue?.message, 'Enter a secret before enabling Exchange Key')
  })

  test('rejects a secret shorter than 16 characters', () => {
    const issue = firstIssue(
      { ...enabledKeepSecret, secret_key: 'x'.repeat(15) },
      unconfiguredView
    )
    assert.equal(issue?.path[0], 'secret_key')
    assert.equal(issue?.message, 'Secret must be at least 16 characters')
  })

  test('accepts a secret that is exactly 16 characters', () => {
    const result = exchangeKeyFormSchema(unconfiguredView).safeParse({
      ...enabledKeepSecret,
      secret_key: 'x'.repeat(16),
    })
    assert.equal(result.success, true)
  })

  test('rejects clearing the secret while Exchange Key stays enabled', () => {
    const issue = firstIssue(
      { ...enabledKeepSecret, secret_key_clear: true },
      storedView
    )
    assert.equal(issue?.path[0], 'secret_key_clear')
    assert.equal(
      issue?.message,
      'Disable Exchange Key before clearing the secret'
    )
  })
})
