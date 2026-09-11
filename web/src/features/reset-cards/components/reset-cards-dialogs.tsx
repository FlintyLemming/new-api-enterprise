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
import { ResetCardsDeleteDialog } from './reset-cards-delete-dialog'
import { ResetCardsDisableDialog } from './reset-cards-disable-dialog'
import { ResetCardsGrantDialog } from './reset-cards-grant-dialog'
import { useResetCards } from './reset-cards-provider'

export function ResetCardsDialogs() {
  const { open, setOpen, triggerRefresh } = useResetCards()

  return (
    <>
      <ResetCardsGrantDialog
        open={open === 'grant'}
        onOpenChange={(isOpen) => !isOpen && setOpen(null)}
        onSuccess={triggerRefresh}
      />
      <ResetCardsDisableDialog />
      <ResetCardsDeleteDialog />
    </>
  )
}
