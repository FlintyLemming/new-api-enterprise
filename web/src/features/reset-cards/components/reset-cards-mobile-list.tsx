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
import type { Table as TanstackTable } from '@tanstack/react-table'
import { Database } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { formatTimestampToDate } from '@/lib/format'

import { RESET_CARD_STATUSES } from '../constants'
import { isResetCardExpired } from '../lib'
import type { ResetCard } from '../types'
import { DataTableRowActions } from './data-table-row-actions'

const MOBILE_SKELETON_KEYS = [
  'reset-card-mobile-skeleton-1',
  'reset-card-mobile-skeleton-2',
  'reset-card-mobile-skeleton-3',
  'reset-card-mobile-skeleton-4',
  'reset-card-mobile-skeleton-5',
]

function ResetCardsMobileSkeleton() {
  return (
    <div className='divide-border overflow-hidden rounded-lg border'>
      {MOBILE_SKELETON_KEYS.map((key) => (
        <div
          key={key}
          className='space-y-2 border-b px-3 py-2.5 last:border-b-0'
        >
          <div className='flex items-center justify-between'>
            <Skeleton className='h-4 w-32' />
            <Skeleton className='h-5 w-16 rounded-md' />
          </div>
          <div className='flex items-center justify-between gap-3'>
            <Skeleton className='h-7 w-44' />
            <Skeleton className='h-8 w-16' />
          </div>
          <Skeleton className='h-3 w-28' />
        </div>
      ))}
    </div>
  )
}

interface ResetCardsMobileListProps {
  table: TanstackTable<ResetCard>
  isLoading: boolean
}

export function ResetCardsMobileList(props: ResetCardsMobileListProps) {
  const { t } = useTranslation()
  const rows = props.table.getRowModel().rows

  if (props.isLoading) return <ResetCardsMobileSkeleton />

  if (!rows.length) {
    return (
      <div className='rounded-lg border p-8'>
        <Empty className='border-none p-0'>
          <EmptyHeader>
            <EmptyMedia variant='icon'>
              <Database className='size-6' />
            </EmptyMedia>
            <EmptyTitle>{t('No Reset Cards Found')}</EmptyTitle>
            <EmptyDescription>
              {t(
                'No reset cards available. Grant your first reset card to get started.'
              )}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </div>
    )
  }

  return (
    <div className='divide-border overflow-hidden rounded-lg border'>
      {rows.map((row) => {
        const card = row.original
        const expired = isResetCardExpired(card.expired_time, card.status)
        const statusConfig = RESET_CARD_STATUSES[card.status]

        return (
          <div
            key={row.id}
            className='bg-card space-y-2.5 border-b px-3 py-2.5 last:border-b-0'
          >
            <div className='flex items-start justify-between gap-3'>
              <div className='min-w-0'>
                <div className='truncate text-sm font-semibold'>
                  {card.name}
                </div>
                <div className='text-muted-foreground text-[11px]'>
                  ID: {card.id} · {t('User ID')}: {card.user_id}
                </div>
              </div>
              {expired ? (
                <StatusBadge
                  label={t('Expired')}
                  variant='warning'
                  copyable={false}
                />
              ) : (
                statusConfig && (
                  <StatusBadge
                    label={t(statusConfig.labelKey)}
                    variant={statusConfig.variant}
                    copyable={false}
                  />
                )
              )}
            </div>

            <div className='flex min-w-0 items-center justify-between gap-2'>
              <span className='text-muted-foreground text-xs'>
                {t('Created At')}: {formatTimestampToDate(card.created_time)}
              </span>
              <DataTableRowActions row={row} />
            </div>
          </div>
        )
      })}
    </div>
  )
}
