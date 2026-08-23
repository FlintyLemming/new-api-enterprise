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
import { RefreshCw } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import { cn } from '@/lib/utils'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useResetForm } from '../hooks/use-reset-form'
import {
  EXCHANGE_KEY_SETTINGS_QUERY_KEY,
  fetchExchangeKeySettings,
  updateExchangeKeySettings,
  type ExchangeKeySettingsView,
} from './exchange-key-api'
import {
  exchangeKeyFormSchema,
  type ExchangeKeyFormValues,
} from './exchange-key-schema'
import { generateExchangeKeySecret } from './exchange-key-secret'
import { ExchangeKeyUsageGuide } from './exchange-key-usage-guide'

const disabledView: ExchangeKeySettingsView = {
  enabled: false,
  secret_configured: false,
  secret_from_env: false,
  enabled_from_env: false,
}

function buildFormDefaults(
  view: ExchangeKeySettingsView
): ExchangeKeyFormValues {
  return {
    enabled: view.enabled,
    secret_key: '',
    secret_key_clear: false,
  }
}

export function ExchangeKeySettingsSection() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [secretRevealed, setSecretRevealed] = useState(false)

  const settingsQuery = useQuery({
    queryKey: EXCHANGE_KEY_SETTINGS_QUERY_KEY,
    queryFn: fetchExchangeKeySettings,
  })
  const persisted = settingsQuery.data ?? disabledView

  const formDefaults = useMemo(() => buildFormDefaults(persisted), [persisted])
  const formSchema = useMemo(
    () => exchangeKeyFormSchema(persisted),
    [persisted]
  )
  const form = useForm<ExchangeKeyFormValues>({
    resolver: zodResolver(formSchema),
    defaultValues: formDefaults,
  })
  useResetForm(form, formDefaults)

  const updateMutation = useMutation({
    mutationFn: updateExchangeKeySettings,
    onSuccess: () => {
      toast.success(t('Setting updated successfully'))
      setSecretRevealed(false)
      form.reset({
        enabled: form.getValues('enabled'),
        secret_key: '',
        secret_key_clear: false,
      })
      queryClient.invalidateQueries({
        queryKey: EXCHANGE_KEY_SETTINGS_QUERY_KEY,
      })
    },
  })

  const values = form.watch()

  const onSubmit = (submitted: ExchangeKeyFormValues) => {
    updateMutation.mutate({
      enabled: submitted.enabled,
      secret_key: submitted.secret_key,
      secret_key_clear: submitted.secret_key_clear,
    })
  }

  return (
    <SettingsSection title={t('Exchange Key')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateMutation.isPending}
            saveLabel='Save Exchange Key settings'
          />

          {persisted.enabled_from_env && (
            <Alert>
              <AlertTitle>{t('Enabled by environment variable')}</AlertTitle>
              <AlertDescription>
                {t(
                  'EXCHANGE_KEY_ENABLED is set on the server, so this switch cannot be changed here.'
                )}
              </AlertDescription>
            </Alert>
          )}

          <FormField
            control={form.control}
            name='enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable Exchange Key')}</FormLabel>
                  <FormDescription>
                    {t(
                      "Allow internal applications to authenticate as an existing user with HMAC of the username. Charges use that user's wallet or subscription."
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={persisted.enabled_from_env}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {values.enabled && form.formState.errors.secret_key_clear && (
            <Alert variant='destructive'>
              <AlertDescription>
                {t(String(form.formState.errors.secret_key_clear.message))}
              </AlertDescription>
            </Alert>
          )}

          {persisted.secret_from_env && (
            <Alert>
              <AlertTitle>{t('Configured by environment variable')}</AlertTitle>
              <AlertDescription>
                {t(
                  'EXCHANGE_KEY_SECRET is set on the server, so the secret cannot be changed here.'
                )}
              </AlertDescription>
            </Alert>
          )}

          <FormField
            control={form.control}
            name='secret_key'
            render={({ field }) => (
              <FormItem data-settings-form-span='full'>
                <FormLabel className='flex items-center gap-2'>
                  {t('Shared secret')}
                  <Badge
                    variant={
                      persisted.secret_configured ? 'default' : 'secondary'
                    }
                  >
                    {persisted.secret_configured
                      ? t('Configured')
                      : t('Not configured')}
                  </Badge>
                </FormLabel>
                <FormControl>
                  <div className='flex min-w-0 flex-wrap items-center gap-2'>
                    <Input
                      type={secretRevealed ? 'text' : 'password'}
                      autoComplete='new-password'
                      spellCheck={false}
                      disabled={persisted.secret_from_env}
                      className={cn(
                        'min-w-0 flex-1',
                        secretRevealed && 'font-mono text-xs'
                      )}
                      {...field}
                    />
                    <Button
                      type='button'
                      variant='outline'
                      disabled={persisted.secret_from_env}
                      onClick={() => {
                        field.onChange(generateExchangeKeySecret())
                        form.setValue('secret_key_clear', false)
                        setSecretRevealed(true)
                      }}
                    >
                      <RefreshCw aria-hidden='true' />
                      {t('Generate')}
                    </Button>
                    {secretRevealed && field.value ? (
                      <CopyButton
                        value={field.value}
                        tooltip={t('Copy secret')}
                        aria-label={t('Copy secret')}
                      />
                    ) : null}
                  </div>
                </FormControl>
                <FormDescription>
                  {secretRevealed
                    ? t(
                        'Copy this secret now. After saving it will not be shown again.'
                      )
                    : t(
                        'Leave empty to keep the stored secret. The secret is never sent back to this page.'
                      )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          {!values.enabled && persisted.secret_configured && (
            <FormField
              control={form.control}
              name='secret_key_clear'
              render={({ field }) => (
                <FormItem className='flex flex-row items-center gap-2'>
                  <FormControl>
                    <Checkbox
                      checked={field.value}
                      onCheckedChange={field.onChange}
                      disabled={persisted.secret_from_env}
                    />
                  </FormControl>
                  <FormLabel>{t('Clear the stored secret key')}</FormLabel>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}

          <ExchangeKeyUsageGuide />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
