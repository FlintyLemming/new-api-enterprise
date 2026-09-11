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
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { searchUsers } from '@/features/users/api'
import type { User } from '@/features/users/types'
import { handleServerError } from '@/lib/handle-server-error'

import { grantResetCards } from '../api'
import { SUCCESS_MESSAGES } from '../constants'
import {
  getResetCardGrantFormSchema,
  RESET_CARD_GRANT_DEFAULT_VALUES,
  transformGrantFormToPayload,
  type ResetCardGrantFormValues,
} from '../lib'

interface ResetCardsGrantDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess?: () => void
}

export function ResetCardsGrantDialog(props: ResetCardsGrantDialogProps) {
  const { t } = useTranslation()
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [userKeyword, setUserKeyword] = useState('')
  const [userOptions, setUserOptions] = useState<User[]>([])
  const [selectedUser, setSelectedUser] = useState<User | null>(null)

  const form = useForm<ResetCardGrantFormValues>({
    resolver: zodResolver(getResetCardGrantFormSchema(t)),
    defaultValues: RESET_CARD_GRANT_DEFAULT_VALUES,
  })

  // 用户搜索（防抖 300ms），复用 features/users 的 searchUsers
  useEffect(() => {
    const handle = setTimeout(async () => {
      try {
        const res = await searchUsers({
          keyword: userKeyword,
          p: 1,
          page_size: 10,
        })
        if (res.success) {
          setUserOptions(res.data?.items ?? [])
        }
      } catch {
        // 忽略搜索失败，保持现有选项
      }
    }, 300)
    return () => clearTimeout(handle)
  }, [userKeyword])

  useEffect(() => {
    if (props.open) {
      form.reset(RESET_CARD_GRANT_DEFAULT_VALUES)
      setSelectedUser(null)
      setUserKeyword('')
    }
  }, [props.open, form])

  const onSubmit = async (data: ResetCardGrantFormValues) => {
    setIsSubmitting(true)
    try {
      const result = await grantResetCards(transformGrantFormToPayload(data))
      if (result.success) {
        toast.success(t(SUCCESS_MESSAGES.RESET_CARDS_GRANTED))
        props.onOpenChange(false)
        props.onSuccess?.()
      }
    } catch (error: unknown) {
      handleServerError(error)
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent className={sideDrawerContentClassName('sm:max-w-[480px]')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>{t('Grant Reset Cards')}</SheetTitle>
          <SheetDescription>
            {t('Grant subscription reset cards directly to a user.')}
          </SheetDescription>
        </SheetHeader>
        <Form {...form}>
          <form
            id='reset-card-grant-form'
            onSubmit={form.handleSubmit(onSubmit)}
            className={sideDrawerFormClassName()}
          >
            <FormField
              control={form.control}
              name='user_id'
              render={() => (
                <FormItem>
                  <FormLabel>{t('User')}</FormLabel>
                  {selectedUser ? (
                    <div className='flex items-center justify-between rounded-md border px-3 py-2 text-sm'>
                      <span>
                        {selectedUser.username} (ID: {selectedUser.id})
                      </span>
                      <Button
                        type='button'
                        variant='ghost'
                        size='sm'
                        onClick={() => {
                          setSelectedUser(null)
                          form.setValue('user_id', 0)
                        }}
                      >
                        {t('Change')}
                      </Button>
                    </div>
                  ) : (
                    <>
                      <Input
                        value={userKeyword}
                        onChange={(e) => setUserKeyword(e.target.value)}
                        placeholder={t('Search user by username...')}
                      />
                      {userOptions.length > 0 && (
                        <div className='divide-border max-h-48 divide-y overflow-y-auto rounded-md border'>
                          {userOptions.map((user) => (
                            <button
                              key={user.id}
                              type='button'
                              className='hover:bg-accent w-full px-3 py-2 text-left text-sm'
                              onClick={() => {
                                setSelectedUser(user)
                                form.setValue('user_id', user.id, {
                                  shouldValidate: true,
                                })
                              }}
                            >
                              {user.username} (ID: {user.id})
                            </button>
                          ))}
                        </div>
                      )}
                    </>
                  )}
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='name'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Name')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      aria-label={t('Name')}
                      placeholder={t('e.g. September compensation card')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='count'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Count')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      aria-label={t('Count')}
                      type='number'
                      onChange={(e) => field.onChange(e.target.valueAsNumber)}
                    />
                  </FormControl>
                  <FormDescription>{t('1-100 cards per grant')}</FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
            <FormField
              control={form.control}
              name='expired_time'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Expiration Time')}</FormLabel>
                  <FormControl>
                    <Input
                      aria-label={t('Expiration Time')}
                      type='datetime-local'
                      value={
                        field.value
                          ? new Date(
                              field.value.getTime() -
                                field.value.getTimezoneOffset() * 60_000
                            )
                              .toISOString()
                              .slice(0, 16)
                          : ''
                      }
                      onChange={(e) => {
                        field.onChange(
                          e.target.value ? new Date(e.target.value) : undefined
                        )
                      }}
                    />
                  </FormControl>
                  <FormDescription>
                    {t('Leave empty for no expiration')}
                  </FormDescription>
                  <FormMessage />
                </FormItem>
              )}
            />
          </form>
        </Form>
        <SheetFooter className={sideDrawerFooterClassName()}>
          <Button
            type='submit'
            form='reset-card-grant-form'
            disabled={isSubmitting}
          >
            {isSubmitting ? t('Granting...') : t('Grant')}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
