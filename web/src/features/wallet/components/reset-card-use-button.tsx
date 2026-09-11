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
import { TicketCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import {
  getSelfResetCardCount,
  redeemSubscriptionResetCard,
} from '@/features/subscriptions/api'
import { handleServerError } from '@/lib/handle-server-error'

interface ResetCardUseButtonProps {
  subscriptionId: number
  /** 核销成功后父组件刷新订阅数据 */
  onUsed: () => void
}

export function ResetCardUseButton(props: ResetCardUseButtonProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)

  const { data } = useQuery({
    queryKey: ['reset-cards', 'self-count'],
    queryFn: async () => {
      const res = await getSelfResetCardCount()
      return res.data?.count ?? 0
    },
  })
  const count = data ?? 0

  const mutation = useMutation({
    mutationFn: () => redeemSubscriptionResetCard(props.subscriptionId),
    onSuccess: (res) => {
      if (res.success) {
        toast.success(t('Subscription quota reset successfully'))
        setConfirmOpen(false)
        queryClient.invalidateQueries({
          queryKey: ['reset-cards', 'self-count'],
        })
        props.onUsed()
      } else {
        toast.error(res.message || t('Operation failed'))
      }
    },
    onError: (error: unknown) => handleServerError(error),
  })

  return (
    <>
      <Button
        size='sm'
        variant='outline'
        disabled={count === 0 || mutation.isPending}
        onClick={() => setConfirmOpen(true)}
      >
        <TicketCheck className='mr-1 size-3.5' aria-hidden='true' />
        {t('Use reset card ({{count}} left)', { count })}
      </Button>
      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('Use subscription reset card?')}
        desc={t(
          'This will clear the used quota of this subscription for the current period and restart the reset cycle.'
        )}
        confirmText={t('Use reset card')}
        handleConfirm={() => mutation.mutate()}
        isLoading={mutation.isPending}
        className='max-w-md'
      />
    </>
  )
}
