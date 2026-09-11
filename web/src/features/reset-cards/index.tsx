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
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'

import { ResetCardsDialogs } from './components/reset-cards-dialogs'
import { ResetCardsPrimaryButtons } from './components/reset-cards-primary-buttons'
import { ResetCardsProvider } from './components/reset-cards-provider'
import { ResetCardsTable } from './components/reset-cards-table'

export function ResetCards() {
  const { t } = useTranslation()
  return (
    <ResetCardsProvider>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Reset Cards')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <ResetCardsPrimaryButtons />
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <ResetCardsTable />
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <ResetCardsDialogs />
    </ResetCardsProvider>
  )
}
