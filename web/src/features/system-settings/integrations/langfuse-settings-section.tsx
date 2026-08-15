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
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from '@/components/ui/accordion'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import { safeNumberFieldProps } from '../utils/numeric-field'
import {
  fetchLangfuseSettings,
  LANGFUSE_SETTINGS_QUERY_KEY,
  updateLangfuseSettings,
  type LangfuseSettingsView,
} from './langfuse-api'
import {
  describeCaptureCapacity,
  formatCaptureBytes,
  LANGFUSE_AVERAGE_CAPTURE_SECONDS,
} from './langfuse-capacity'
import {
  LANGFUSE_ENABLE_ERRORS,
  LANGFUSE_SESSION_PATH_PRESETS,
  langfuseFormSchema,
  splitConfiguredLines,
  validateEnableTransition,
  type LangfuseFormValues,
} from './langfuse-schema'

// Mirrors setting/langfuse_setting.DefaultLangfuseSetting so the form renders a
// coherent disabled draft before the first response arrives.
const disabledView: LangfuseSettingsView = {
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

function buildFormDefaults(view: LangfuseSettingsView): LangfuseFormValues {
  return {
    enabled: view.enabled,
    host: view.host,
    public_key: view.public_key,
    secret_key: '',
    secret_key_clear: false,
    environment: view.environment,
    sample_rate: view.sample_rate,
    send_content: view.send_content,
    max_content_bytes: view.max_content_bytes,
    max_response_bytes: view.max_response_bytes,
    max_in_flight_capture_bytes: view.max_in_flight_capture_bytes,
    max_session_body_bytes: view.max_session_body_bytes,
    session_header_names: view.session_header_names.join('\n'),
    session_body_paths: view.session_body_paths.join('\n'),
    queue_size: view.queue_size,
    batch_size: view.batch_size,
    flush_interval_seconds: view.flush_interval_seconds,
  }
}

export function LangfuseSettingsSection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [sampleRateConfirmed, setSampleRateConfirmed] = useState(false)
  const [sendContentConfirmed, setSendContentConfirmed] = useState(false)
  const [confirmationError, setConfirmationError] = useState<string | null>(
    null
  )

  const settingsQuery = useQuery({
    queryKey: LANGFUSE_SETTINGS_QUERY_KEY,
    queryFn: fetchLangfuseSettings,
  })
  const persisted = settingsQuery.data ?? disabledView

  const formDefaults = useMemo(() => buildFormDefaults(persisted), [persisted])
  const form = useForm<LangfuseFormValues>({
    resolver: zodResolver(langfuseFormSchema),
    defaultValues: formDefaults,
  })
  useResetForm(form, formDefaults)

  const updateMutation = useMutation({
    mutationFn: updateLangfuseSettings,
    onSuccess: () => {
      // The backend answers a successful PUT without a body, so the persisted
      // view is refetched instead of patched locally.
      toast.success(t('Setting updated successfully'))
      setSampleRateConfirmed(false)
      setSendContentConfirmed(false)
      queryClient.invalidateQueries({ queryKey: LANGFUSE_SETTINGS_QUERY_KEY })
    },
  })

  const values = form.watch()
  const capacity = describeCaptureCapacity(values)
  const selectedBodyPaths = new Set(
    splitConfiguredLines(values.session_body_paths)
  )
  const showEnableConfirmation = !persisted.enabled && values.enabled

  const toggleBodyPathPreset = (preset: string, checked: boolean) => {
    const current = splitConfiguredLines(values.session_body_paths)
    const next = checked
      ? [...current.filter((path) => path !== preset), preset]
      : current.filter((path) => path !== preset)
    form.setValue('session_body_paths', next.join('\n'), {
      shouldDirty: true,
      shouldValidate: true,
    })
  }

  const onSubmit = (submitted: LangfuseFormValues) => {
    const transitionError = validateEnableTransition(persisted, {
      ...submitted,
      sample_rate_confirmed: sampleRateConfirmed,
      send_content_confirmed: sendContentConfirmed,
    })
    if (transitionError === LANGFUSE_ENABLE_ERRORS.secretKey) {
      form.setError('secret_key', { message: transitionError })
      return
    }
    if (transitionError) {
      setConfirmationError(transitionError)
      return
    }

    setConfirmationError(null)
    updateMutation.mutate({
      enabled: submitted.enabled,
      host: submitted.host.trim(),
      public_key: submitted.public_key.trim(),
      secret_key: submitted.secret_key,
      secret_key_clear: submitted.secret_key_clear,
      environment: submitted.environment,
      sample_rate: submitted.sample_rate,
      send_content: submitted.send_content,
      max_content_bytes: submitted.max_content_bytes,
      max_response_bytes: submitted.max_response_bytes,
      max_in_flight_capture_bytes: submitted.max_in_flight_capture_bytes,
      max_session_body_bytes: submitted.max_session_body_bytes,
      session_header_names: splitConfiguredLines(
        submitted.session_header_names
      ),
      session_body_paths: splitConfiguredLines(submitted.session_body_paths),
      queue_size: submitted.queue_size,
      batch_size: submitted.batch_size,
      flush_interval_seconds: submitted.flush_interval_seconds,
    })
  }

  return (
    <SettingsSection title={t('Langfuse Tracing')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateMutation.isPending}
            saveLabel='Save Langfuse settings'
          />

          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable Langfuse tracing')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Export request traces to a Langfuse instance over OTLP. Relay behaviour is unaffected when the exporter is unavailable.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={(checked) => {
                      field.onChange(checked)
                      if (!checked) setConfirmationError(null)
                    }}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {values.enabled &&
            form.formState.errors.secret_key_clear && (
              // The clear checkbox is hidden while tracing is on, so its own
              // message would never reach an administrator who ticked it, turned
              // tracing back on and saved.
              <Alert variant='destructive'>
                <AlertDescription>
                  {t(String(form.formState.errors.secret_key_clear.message))}
                </AlertDescription>
              </Alert>
            )}

          {showEnableConfirmation && (
            <Alert variant={confirmationError ? 'destructive' : 'default'}>
              <AlertTitle>
                {t('Confirm the capture settings before enabling')}
              </AlertTitle>
              <AlertDescription className='space-y-1'>
                <p>
                  {t(
                    'Sampling drops every trace that is not selected, so unsampled requests leave no trace at all.'
                  )}
                </p>
                <p>
                  {t(
                    'With Send content on, prompts and model responses are sent to the external Langfuse instance you configured.'
                  )}
                </p>
                <p>
                  {t(
                    'A sample rate of 0 keeps the configuration and pauses collection.'
                  )}
                </p>
                <p>
                  {sampleRateConfirmed
                    ? t('Sample rate confirmed')
                    : t('Choose a sample rate to continue')}
                  {' · '}
                  {sendContentConfirmed
                    ? t('Content decision confirmed')
                    : t('Choose whether to send content to continue')}
                </p>
                {confirmationError && <p>{t(confirmationError)}</p>}
              </AlertDescription>
            </Alert>
          )}

          <FormField
            control={form.control}
            name='host'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Langfuse host')}</FormLabel>
                <FormControl>
                  <Input
                    placeholder='https://langfuse.example.com'
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Base URL only. The OTLP traces path is appended automatically.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='public_key'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Public key')}</FormLabel>
                <FormControl>
                  <Input placeholder='pk-lf-...' {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='secret_key'
            render={({ field }) => (
              <FormItem>
                <FormLabel className='flex items-center gap-2'>
                  {t('Secret key')}
                  <Badge
                    variant={
                      persisted.secret_key_configured ? 'default' : 'secondary'
                    }
                  >
                    {persisted.secret_key_configured
                      ? t('Configured')
                      : t('Not configured')}
                  </Badge>
                </FormLabel>
                <FormControl>
                  <Input
                    type='password'
                    autoComplete='new-password'
                    placeholder='sk-lf-...'
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Leave empty to keep the stored secret. The secret is never sent back to this page.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {!values.enabled && persisted.secret_key_configured && (
            <FormField
              control={form.control}
              name='secret_key_clear'
              render={({ field }) => (
                <FormItem className='flex flex-row items-center gap-2'>
                  <FormControl>
                    <Checkbox
                      checked={field.value}
                      onCheckedChange={field.onChange}
                    />
                  </FormControl>
                  <FormLabel>{t('Clear the stored secret key')}</FormLabel>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}

          <FormField
            control={form.control}
            name='environment'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Environment')}</FormLabel>
                <FormControl>
                  <Input placeholder='default' {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='sample_rate'
            render={({ field }) => {
              const numberProps = safeNumberFieldProps(field)
              return (
                <FormItem>
                  <FormLabel>{t('Sample rate')}</FormLabel>
                  <FormControl>
                    <Input
                      type='number'
                      min={0}
                      max={1}
                      step={0.05}
                      {...numberProps}
                      onChange={(event) => {
                        setSampleRateConfirmed(true)
                        setConfirmationError(null)
                        numberProps.onChange(event)
                      }}
                    />
                  </FormControl>
                  <FormDescription>
                    {t(
                      'Share of requests captured, between 0 and 1. Sampling is per user-scoped session when one is available, otherwise per request.'
                    )}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )
            }}
          />

          <FormField
            control={form.control}
            name='send_content'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Send content')}</FormLabel>
                  <FormDescription>
                    {t(
                      'With content off, user and session, model, latency, usage, cost and errors are still exported.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={(checked) => {
                      setSendContentConfirmed(true)
                      setConfirmationError(null)
                      field.onChange(checked)
                    }}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          <FormField
            control={form.control}
            name='max_content_bytes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Input and output capture limit')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={4096}
                    max={4194304}
                    step={1024}
                    {...safeNumberFieldProps(field)}
                  />
                </FormControl>
                <FormDescription>
                  {t('Bytes per captured side')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='max_response_bytes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Response capture limit')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={65536}
                    max={8388608}
                    step={1024}
                    {...safeNumberFieldProps(field)}
                  />
                </FormControl>
                <FormDescription>
                  {t('Bytes of upstream response body buffered per request')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='max_in_flight_capture_bytes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Global capture budget')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1}
                    step={1048576}
                    {...safeNumberFieldProps(field)}
                  />
                </FormControl>
                <FormDescription>
                  {t('Bytes reserved across all concurrent captures')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='max_session_body_bytes'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Session body read limit')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    min={1024}
                    max={65536}
                    step={1024}
                    {...safeNumberFieldProps(field)}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Largest request body fully read to look up a session JSON path'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <div
            data-settings-form-span='full'
            data-testid='langfuse-capacity-hint'
            className='bg-muted/20 rounded-xl border px-3 py-2.5'
          >
            <p className='text-sm font-medium'>{t('Capacity estimate')}</p>
            <ul className='text-muted-foreground mt-1 space-y-0.5 text-xs'>
              <li>
                {t('Per-request reservation')}:{' '}
                {formatCaptureBytes(capacity.reservationBytes)}
              </li>
              <li>
                {t('Concurrent capture slots')}: {capacity.captureSlots}
              </li>
              <li>
                {t('Sampled requests per second')}: {capacity.sampledRps}
              </li>
              <li>
                {t('Span bodies resident')}:{' '}
                {formatCaptureBytes(capacity.residentBytes)}
              </li>
              <li>
                {t('Span bodies while exporting')}:{' '}
                {formatCaptureBytes(capacity.exportBytes)}
              </li>
            </ul>
            <p className='text-muted-foreground mt-1 text-xs'>
              {t(
                'Throughput assumes an average capture lifetime of {{seconds}} seconds. These are estimates for sizing the limits: the budget is reserved up front rather than billed per response, and it does not replace whole-process memory planning.',
                { seconds: LANGFUSE_AVERAGE_CAPTURE_SECONDS }
              )}
            </p>
          </div>

          <FormField
            control={form.control}
            name='session_header_names'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Additional session headers')}</FormLabel>
                <FormControl>
                  <Textarea rows={3} {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'One header name per line. Credential headers are not accepted.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='session_body_paths'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Session body paths')}</FormLabel>
                <FormControl>
                  <Textarea rows={3} {...field} />
                </FormControl>
                <FormDescription>
                  {t(
                    'One JSON path per line. Requests without a usable session header trigger a full body read of up to 64 KiB.'
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <div data-settings-form-span='full' className='space-y-2'>
            <p className='text-sm font-medium'>{t('Session path presets')}</p>
            <div className='flex flex-wrap gap-4'>
              {LANGFUSE_SESSION_PATH_PRESETS.map((preset) => (
                <div key={preset} className='flex items-center gap-2'>
                  <Checkbox
                    aria-label={preset}
                    checked={selectedBodyPaths.has(preset)}
                    onCheckedChange={(checked) =>
                      toggleBodyPathPreset(preset, checked === true)
                    }
                  />
                  <span className='font-mono text-xs'>{preset}</span>
                </div>
              ))}
            </div>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Client session values are exported user-scoped as {userId}:{rawSessionId}, so the same raw value only groups within one New API user and is omitted without a positive user ID.'
              )}
            </p>
          </div>

          <Accordion
            data-settings-form-span='full'
            className='rounded-lg border px-3'
          >
            <AccordionItem value='advanced'>
              <AccordionTrigger>{t('Advanced export tuning')}</AccordionTrigger>
              <AccordionContent className='grid gap-4 md:grid-cols-3'>
                <FormField
                  control={form.control}
                  name='queue_size'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Queue size')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={16}
                          max={256}
                          step={1}
                          {...safeNumberFieldProps(field)}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='batch_size'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Batch size')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={1}
                          max={32}
                          step={1}
                          {...safeNumberFieldProps(field)}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
                <FormField
                  control={form.control}
                  name='flush_interval_seconds'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Flush interval (seconds)')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={1}
                          max={300}
                          step={1}
                          {...safeNumberFieldProps(field)}
                        />
                      </FormControl>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </AccordionContent>
            </AccordionItem>
          </Accordion>
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
