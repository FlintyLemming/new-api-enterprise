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
const { LANGFUSE_SETTINGS_QUERY_KEY } = await import('../langfuse-api')
const { SettingsPageProvider } =
  await import('../../components/settings-page-context')
const { LangfuseSettingsSection } = await import('../langfuse-settings-section')
const { LANGFUSE_ENABLE_ERRORS, LANGFUSE_VALIDATION_MESSAGES } =
  await import('../langfuse-schema')

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
type LangfuseView = {
  enabled: boolean
  host: string
  public_key: string
  secret_key_configured: boolean
  environment: string
  sample_rate: number
  send_content: boolean
  max_content_bytes: number
  max_response_bytes: number
  max_in_flight_capture_bytes: number
  max_session_body_bytes: number
  session_header_names: string[]
  session_body_paths: string[]
  queue_size: number
  batch_size: number
  flush_interval_seconds: number
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPut = apiClient.put

const configuredView: LangfuseView = {
  enabled: false,
  host: 'https://langfuse.example.com',
  public_key: 'pk-lf-1',
  secret_key_configured: true,
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

let rendered: {
  host: HTMLDivElement
  actions: HTMLDivElement
  root: ReturnType<typeof createRoot>
  queryClient: InstanceType<typeof QueryClient>
} | null = null

function installApiFixtures(view: LangfuseView, saved: unknown[]) {
  apiClient.get = async (url) => {
    assert.equal(url, '/api/option/langfuse')
    return { data: { success: true, message: '', data: view } }
  }
  apiClient.put = async (url, data) => {
    assert.equal(url, '/api/option/langfuse')
    saved.push(data)
    return { data: { success: true, message: '' } }
  }
}

async function renderSection(view: LangfuseView): Promise<unknown[]> {
  const saved: unknown[] = []
  installApiFixtures(view, saved)

  const host = document.createElement('div')
  const actions = document.createElement('div')
  document.body.append(host, actions)
  const root = createRoot(host)
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(LANGFUSE_SETTINGS_QUERY_KEY, view, {
    updatedAt: Date.now() + 60_000,
  })
  rendered = { host, actions, root, queryClient }

  await act(async () =>
    root.render(
      <QueryClientProvider client={queryClient}>
        <I18nextProvider i18n={i18n}>
          <SettingsPageProvider actionsContainer={actions}>
            <LangfuseSettingsSection />
          </SettingsPageProvider>
        </I18nextProvider>
      </QueryClientProvider>
    )
  )
  assert.equal(findInput('Langfuse host').value, view.host)
  return saved
}

// Waits for a stated condition rather than a fixed delay: a save has to travel
// through form validation and a mutation before the request fixture records it.
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

function findTextarea(labelText: string): HTMLTextAreaElement {
  const textarea = findFormItem(labelText).querySelector('textarea')
  assert.ok(textarea, `Expected a textarea for "${labelText}"`)
  return textarea
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

function findPresetCheckbox(preset: string): HTMLElement {
  const control = document.querySelector<HTMLElement>(
    `[role="checkbox"][aria-label="${preset}"]`
  )
  assert.ok(control, `Expected a preset checkbox for "${preset}"`)
  return control
}

function findSaveButton(): HTMLButtonElement {
  const button = [
    ...document.querySelectorAll<HTMLButtonElement>('button'),
  ].find((candidate) =>
    candidate.textContent?.includes('Save Langfuse settings')
  )
  assert.ok(button, 'Expected the save button')
  return button
}

function capacityHintText(): string {
  const hint = document.querySelector('[data-testid="langfuse-capacity-hint"]')
  assert.ok(hint, 'Expected the capacity hint')
  return hint.textContent ?? ''
}

async function changeInput(
  input: HTMLInputElement | HTMLTextAreaElement,
  value: string
) {
  await act(async () => {
    const prototype =
      input instanceof domWindow.HTMLTextAreaElement
        ? domWindow.HTMLTextAreaElement.prototype
        : domWindow.HTMLInputElement.prototype
    const valueSetter = Object.getOwnPropertyDescriptor(prototype, 'value')?.set
    assert.ok(valueSetter)
    valueSetter.call(input, value)
    input.dispatchEvent(
      new domWindow.Event('input', { bubbles: true }) as unknown as Event
    )
  })
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

describe('Langfuse settings section', () => {
  test('refuses to save a freshly enabled configuration before both decisions are made', async () => {
    const saved = await renderSection(configuredView)

    await act(async () => findSwitch('Enable Langfuse tracing').click())
    await act(async () => findSaveButton().click())

    assert.deepEqual(saved, [])
    assert.ok(
      document.body.textContent?.includes(LANGFUSE_ENABLE_ERRORS.sampleRate),
      'expected the pending sample rate decision to be reported'
    )
  })

  test('sends an explicit sample rate of zero once both decisions are made', async () => {
    const saved = await renderSection(configuredView)

    await act(async () => findSwitch('Enable Langfuse tracing').click())
    await changeInput(findInput('Sample rate'), '0')
    await act(async () => findSwitch('Send content').click())
    await act(async () => findSaveButton().click())
    await waitForCondition(() => saved.length === 1, 'the update was not sent')

    assert.deepEqual(saved[0], {
      enabled: true,
      host: 'https://langfuse.example.com',
      public_key: 'pk-lf-1',
      secret_key: '',
      secret_key_clear: false,
      environment: 'default',
      sample_rate: 0,
      send_content: true,
      max_content_bytes: 65536,
      max_response_bytes: 524288,
      max_in_flight_capture_bytes: 536870912,
      max_session_body_bytes: 65536,
      session_header_names: [],
      session_body_paths: [],
      queue_size: 64,
      batch_size: 16,
      flush_interval_seconds: 5,
    })
  })

  test('leaves every session path preset unselected and adds only the chosen path', async () => {
    const saved = await renderSection(configuredView)

    for (const preset of [
      'metadata.session_id',
      'metadata.conversation_id',
      'conversation_id',
      'chat_id',
    ]) {
      assert.equal(
        findPresetCheckbox(preset).getAttribute('aria-checked'),
        'false',
        `${preset} should start unselected`
      )
    }

    await act(async () => findPresetCheckbox('chat_id').click())
    assert.equal(findTextarea('Session body paths').value, 'chat_id')

    await act(async () => findSaveButton().click())
    await waitForCondition(() => saved.length === 1, 'the update was not sent')

    assert.deepEqual(
      (saved[0] as { session_body_paths: string[] }).session_body_paths,
      ['chat_id']
    )
  })

  test('reports a secret wipe that survived re-enabling instead of saving it', async () => {
    const saved = await renderSection({ ...configuredView, enabled: true })

    await act(async () => findSwitch('Enable Langfuse tracing').click())
    await act(async () => findCheckbox('Clear the stored secret key').click())
    await act(async () => findSwitch('Enable Langfuse tracing').click())
    await act(async () => findSaveButton().click())

    assert.deepEqual(saved, [])
    assert.ok(
      document.body.textContent?.includes(
        LANGFUSE_VALIDATION_MESSAGES.secretClearRequiresDisabled
      ),
      'expected the secret wipe to be reported'
    )
  })

  test('recomputes the capacity estimate from the configured limits', async () => {
    await renderSection(configuredView)

    const defaults = capacityHintText()
    assert.ok(defaults.includes('640 KiB'), defaults)
    assert.ok(defaults.includes('819'), defaults)
    assert.ok(defaults.includes('27.3'), defaults)
    assert.ok(defaults.includes('552 MiB'), defaults)
    assert.ok(defaults.includes('582 MiB'), defaults)

    await changeInput(findInput('Response capture limit'), '1048576')

    const raised = capacityHintText()
    assert.ok(raised.includes('1.1 MiB'), raised)
    assert.ok(raised.includes('455'), raised)
    assert.ok(raised.includes('15.2'), raised)
  })
})
