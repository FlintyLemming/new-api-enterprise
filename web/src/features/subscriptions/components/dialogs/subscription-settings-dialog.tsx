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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  getAdminPlans,
  getSubscriptionSettings,
  updateSubscriptionSettings,
} from '../../api'
import { useSubscriptions } from '../subscriptions-provider'

const SUBSCRIPTION_SETTINGS_QUERY_KEY = ['admin-subscription-settings']

export function SubscriptionSettingsDialog() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { open, setOpen, refreshTrigger } = useSubscriptions()
  const isOpen = open === 'settings'
  const selectId = useId()
  const [planId, setPlanId] = useState('0')

  const settingsQuery = useQuery({
    queryKey: SUBSCRIPTION_SETTINGS_QUERY_KEY,
    queryFn: async () =>
      requireServerSuccess(await getSubscriptionSettings()).data,
    enabled: isOpen,
  })
  const plansQuery = useQuery({
    queryKey: ['admin-subscription-plans', refreshTrigger],
    queryFn: async () => requireServerSuccess(await getAdminPlans()).data || [],
    enabled: isOpen,
  })

  const savedPlanId = settingsQuery.data?.new_user_plan_id
  useEffect(() => {
    if (isOpen && savedPlanId !== undefined) setPlanId(String(savedPlanId))
  }, [isOpen, savedPlanId])

  const mutation = useMutation({
    mutationFn: async (newUserPlanId: number) =>
      requireServerSuccess(
        await updateSubscriptionSettings({ new_user_plan_id: newUserPlanId })
      ),
    onSuccess: () => {
      toast.success(t('Saved successfully'))
      queryClient.invalidateQueries({
        queryKey: SUBSCRIPTION_SETTINGS_QUERY_KEY,
      })
      setOpen(null)
    },
    onError: (error) => handleServerError(error, t('Operation failed')),
  })

  const plans = plansQuery.data || []
  const savedPlanMissing =
    planId !== '0' && !plans.some((record) => String(record.plan.id) === planId)

  return (
    <Dialog
      open={isOpen}
      onOpenChange={(nextOpen) => !nextOpen && setOpen(null)}
      title={t('Subscription settings')}
      footer={
        <>
          <Button variant='outline' onClick={() => setOpen(null)}>
            {t('Cancel')}
          </Button>
          <Button
            onClick={() => mutation.mutate(Number(planId))}
            disabled={settingsQuery.isLoading || mutation.isPending}
          >
            {t('Save')}
          </Button>
        </>
      }
    >
      <FieldGroup>
        <Field>
          <FieldLabel htmlFor={selectId}>
            {t('New user default plan')}
          </FieldLabel>
          <NativeSelect
            id={selectId}
            className='w-full'
            value={planId}
            onChange={(event) => setPlanId(event.target.value)}
            disabled={settingsQuery.isLoading}
          >
            <NativeSelectOption value='0'>
              {t('Do not grant')}
            </NativeSelectOption>
            {plans.map((record) => (
              <NativeSelectOption
                key={record.plan.id}
                value={String(record.plan.id)}
              >
                {record.plan.enabled
                  ? record.plan.title
                  : `${record.plan.title} (${t('Disabled')})`}
              </NativeSelectOption>
            ))}
            {savedPlanMissing ? (
              <NativeSelectOption value={planId}>#{planId}</NativeSelectOption>
            ) : null}
          </NativeSelect>
          <FieldDescription>
            {t(
              'New accounts, including those created by SSO sign-in, automatically receive this plan. Existing users are not affected.'
            )}
          </FieldDescription>
        </Field>
      </FieldGroup>
    </Dialog>
  )
}
