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
import { after, afterEach, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLButtonElement',
  'HTMLInputElement',
  'HTMLTextAreaElement',
  'HTMLFormElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'KeyboardEvent',
  'PointerEvent',
  'MouseEvent',
  'FocusEvent',
  'CustomEvent',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { EXCHANGE_KEY_SETTINGS_QUERY_KEY } = await import('../exchange-key-api')
const { SettingsPageProvider } =
  await import('../../components/settings-page-context')
const { ExchangeKeySettingsSection } =
  await import('../exchange-key-settings-section')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; put: ApiMethod }
type ExchangeKeyView = {
  enabled: boolean
  secret_configured: boolean
  secret_from_env: boolean
  enabled_from_env: boolean
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put

const configuredView: ExchangeKeyView = {
  enabled: true,
  secret_configured: true,
  secret_from_env: false,
  enabled_from_env: false,
}

let rendered: {
  host: HTMLDivElement
  actions: HTMLDivElement
  root: ReturnType<typeof createRoot>
  queryClient: InstanceType<typeof QueryClient>
} | null = null

function installApiFixtures(view: ExchangeKeyView, saved: unknown[]) {
  apiClient.get = async (url) => {
    assert.equal(url, '/api/option/exchange-key')
    return { data: { success: true, message: '', data: view } }
  }
  apiClient.put = async (url, data) => {
    assert.equal(url, '/api/option/exchange-key')
    saved.push(data)
    return { data: { success: true, message: '' } }
  }
}

async function renderSection(view: ExchangeKeyView): Promise<unknown[]> {
  const saved: unknown[] = []
  installApiFixtures(view, saved)

  const host = document.createElement('div')
  const actions = document.createElement('div')
  document.body.append(host, actions)
  const root = createRoot(host)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(EXCHANGE_KEY_SETTINGS_QUERY_KEY, view, {
    updatedAt: Date.now() + 60_000,
  })
  rendered = { host, actions, root, queryClient }

  await act(async () =>
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <SettingsPageProvider actionsContainer={actions}>
            <ExchangeKeySettingsSection />
          </SettingsPageProvider>
        </I18nextProvider>
      </QueryClientProvider>
    )
  )
  assert.ok(findInput('Shared secret'))
  return saved
}

async function waitForCondition(
  condition: () => boolean,
  failureMessage: string
): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (condition()) return
    await act(
      async () => await new Promise((resolve) => setTimeout(resolve, 10))
    )
  }
  throw new Error(`${failureMessage}: ${document.body.textContent}`)
}

function findFormItem(labelText: string): HTMLElement {
  const label = [...document.querySelectorAll<HTMLLabelElement>('label')].find(
    (candidate) => candidate.textContent?.trim().startsWith(labelText)
  )
  assert.ok(label, `Expected a field labelled "${labelText}"`)
  const item = label.closest<HTMLElement>('[data-slot="form-item"]')
  assert.ok(item, `Expected "${labelText}" to sit in a form item`)
  return item
}

function findInput(labelText: string): HTMLInputElement {
  const input = findFormItem(labelText).querySelector('input')
  assert.ok(input, `Expected an input for "${labelText}"`)
  return input
}

function findSwitch(labelText: string): HTMLElement {
  const control =
    findFormItem(labelText).querySelector<HTMLElement>('[role="switch"]')
  assert.ok(control, `Expected a switch for "${labelText}"`)
  return control
}

function findCheckbox(labelText: string): HTMLElement {
  const control =
    findFormItem(labelText).querySelector<HTMLElement>('[role="checkbox"]')
  assert.ok(control, `Expected a checkbox for "${labelText}"`)
  return control
}

async function changeInput(input: HTMLInputElement, value: string) {
  await act(async () => {
    const valueSetter = Object.getOwnPropertyDescriptor(
      domWindow.HTMLInputElement.prototype,
      'value'
    )?.set
    assert.ok(valueSetter)
    valueSetter.call(input, value)
    input.dispatchEvent(
      new domWindow.Event('input', { bubbles: true }) as unknown as Event
    )
  })
}

function findSaveButton(): HTMLButtonElement {
  const button = [
    ...document.querySelectorAll<HTMLButtonElement>('button'),
  ].find((candidate) =>
    candidate.textContent?.includes('Save Exchange Key settings')
  )
  assert.ok(button, 'Expected the save button')
  return button
}

function isDisabledControl(element: HTMLElement): boolean {
  return (
    element.getAttribute('aria-disabled') === 'true' ||
    element.hasAttribute('data-disabled')
  )
}

afterEach(async () => {
  apiClient.get = originalGet
  apiClient.put = originalPut
  if (rendered) {
    await act(async () => rendered?.root.unmount())
    rendered.queryClient.clear()
    rendered.host.remove()
    rendered.actions.remove()
    rendered = null
  }
  document.body.replaceChildren()
})

after(() => {
  domWindow.close()
})

describe('Exchange Key settings section', () => {
  test('disables the secret input when the secret is locked by the environment', async () => {
    await renderSection({
      ...configuredView,
      secret_from_env: true,
    })

    assert.equal(findInput('Shared secret').disabled, true)
    assert.ok(
      document.body.textContent?.includes(
        'EXCHANGE_KEY_SECRET is set on the server, so the secret cannot be changed here.'
      ),
      'expected the environment secret lock to be reported'
    )
  })

  test('sends an empty secret_key to keep the stored secret', async () => {
    const saved = await renderSection(configuredView)

    await act(async () => findSaveButton().click())
    await waitForCondition(() => saved.length === 1, 'the update was not sent')

    assert.deepEqual(saved[0], {
      enabled: true,
      secret_key: '',
      secret_key_clear: false,
    })
  })

  test('disables the enable switch when the flag is locked by the environment', async () => {
    await renderSection({
      ...configuredView,
      enabled_from_env: true,
    })

    assert.equal(isDisabledControl(findSwitch('Enable Exchange Key')), true)
    assert.ok(
      document.body.textContent?.includes(
        'EXCHANGE_KEY_ENABLED is set on the server, so this switch cannot be changed here.'
      ),
      'expected the environment enable lock to be reported'
    )
  })

  test('resets the clear-secret flag after a successful save so a new secret can be stored', async () => {
    const saved = await renderSection({
      enabled: false,
      secret_configured: true,
      secret_from_env: false,
      enabled_from_env: false,
    })

    await act(async () => findCheckbox('Clear the stored secret key').click())
    await act(async () => findSaveButton().click())
    await waitForCondition(
      () => saved.length === 1,
      'the clear update was not sent'
    )

    assert.deepEqual(saved[0], {
      enabled: false,
      secret_key: '',
      secret_key_clear: true,
    })

    const clearedView: ExchangeKeyView = {
      enabled: false,
      secret_configured: false,
      secret_from_env: false,
      enabled_from_env: false,
    }
    installApiFixtures(clearedView, saved)
    await act(async () => {
      rendered?.queryClient.setQueryData(
        EXCHANGE_KEY_SETTINGS_QUERY_KEY,
        clearedView,
        { updatedAt: Date.now() + 60_000 }
      )
    })

    const newSecret = 'sixteen-char-key'
    await act(async () => findSwitch('Enable Exchange Key').click())
    await changeInput(findInput('Shared secret'), newSecret)
    await act(async () => findSaveButton().click())

    assert.ok(
      !document.body.textContent?.includes(
        'Disable Exchange Key before clearing the secret'
      ),
      'expected the leftover clear flag not to block enabling with a new secret'
    )
    await waitForCondition(
      () => saved.length === 2,
      'the enable update was not sent'
    )

    assert.deepEqual(saved[1], {
      enabled: true,
      secret_key: newSecret,
      secret_key_clear: false,
    })
  })
})
