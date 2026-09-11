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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'

import { disableResetCards } from '../api'
import { SUCCESS_MESSAGES } from '../constants'
import { useResetCards } from './reset-cards-provider'

export function ResetCardsDisableDialog() {
  const { t } = useTranslation()
  const { open, setOpen, currentRow, triggerRefresh } = useResetCards()
  const [isDisabling, setIsDisabling] = useState(false)

  const handleDisable = async () => {
    if (!currentRow) return

    setIsDisabling(true)
    try {
      const result = await disableResetCards([currentRow.id])
      if (result.success) {
        toast.success(t(SUCCESS_MESSAGES.RESET_CARD_DISABLED))
        setOpen(null)
        triggerRefresh()
      }
    } finally {
      setIsDisabling(false)
    }
  }

  return (
    <ConfirmDialog
      destructive
      open={open === 'disable'}
      onOpenChange={(isOpen) => !isOpen && setOpen(null)}
      handleConfirm={handleDisable}
      isLoading={isDisabling}
      className='max-w-md'
      title={t('Disable Reset Card?')}
      desc={t(
        'This will disable the reset card. The user will no longer be able to use it.'
      )}
      confirmText={t('Disable')}
    />
  )
}
