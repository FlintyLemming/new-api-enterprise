# 订阅重置卡 Implementation Plan — Frontend（Task 6–10）

> 本文件是 `docs/superpowers/plans/2026-09-11-subscription-reset-card.md` 的拆分文件。先读主计划（Global Constraints 对所有任务生效）与 spec。backend 文件（Task 1–5）必须先完成，前端依赖其 API 契约。

**本部分目标：** 管理端 `/reset-cards` 页面（列表/搜索/发放/禁用/删除）+ 钱包页订阅卡片"使用重置卡"按钮。

**通用约束（每个任务都适用）：**
- 每个新建的 `web/src` 文件必须以标准 AGPL 头开头（从 `web/src/features/redemption-codes/api.ts` 等既有文件逐字复制，lines 1–18），否则 `bun run format:check` 会失败。
- 所有面向用户文案用 `t('English key')`；常量里的文案用 `labelKey`/i18n 键 + `t()`。
- 每个任务结束前：`cd web && bun run typecheck && bunx oxlint -c .oxlintrc.json <改动文件>` 无 error。
- 测试模式：不 `vi.mock('@/lib/api')`，而是顶层 `await import('@/lib/api')` 拿到实例后猴子补丁 `api.get/post`，`afterEach` 恢复原方法（参考 `web/src/features/redemption-codes/components/__tests__/redemptions-mutate-drawer.test.tsx`）。

---

### Task 6: reset-cards feature 基础（types / api / constants / lib）

**Files:**
- Create: `web/src/features/reset-cards/types.ts`
- Create: `web/src/features/reset-cards/api.ts`
- Create: `web/src/features/reset-cards/constants.ts`
- Create: `web/src/features/reset-cards/lib/utils.ts`
- Create: `web/src/features/reset-cards/lib/reset-card-form.ts`
- Create: `web/src/features/reset-cards/lib/index.ts`
- Modify: `web/src/i18n/static-keys.ts`（登记常量持有的 i18n 键）

**Interfaces:**
- Consumes: 后端 Task 5 的 API 契约；`@/lib/api` 的 axios 实例；`@/components/status-badge` 的 `StatusBadgeProps`。
- Produces（Task 7/8/9 全部依赖）：
  - `resetCardSchema`、`type ResetCard`、`ApiResponse<T>`、`GetResetCardsParams/Response`、`SearchResetCardsParams`、`GrantResetCardsPayload`、`type ResetCardsDialogType = 'grant' | 'disable' | 'delete'`
  - `getResetCards(params?)`、`searchResetCards(params)`、`grantResetCards(payload)`、`disableResetCards(ids)`、`deleteResetCard(id)`
  - `RESET_CARD_STATUS {UNUSED:1, USED:2, DISABLED:3}`、`RESET_CARD_STATUSES`（labelKey+variant）、`RESET_CARD_FILTER_EXPIRED='expired'`、`RESET_CARD_FILTER_VALUES`、`getResetCardStatusOptions(t)`、`RESET_CARD_VALIDATION`、`ERROR_MESSAGES`、`SUCCESS_MESSAGES`、`getResetCardFormErrorMessages(t)`
  - `isResetCardExpired(expired_time, status)`、`getResetCardGrantFormSchema(t)`、`type ResetCardGrantFormValues`、`RESET_CARD_GRANT_DEFAULT_VALUES`、`transformGrantFormToPayload(data)`

- [ ] **Step 1: types.ts**

```ts
import { z } from 'zod'

export const resetCardSchema = z.object({
  id: z.number(),
  name: z.string(),
  user_id: z.number(),
  status: z.number(),
  created_time: z.number(),
  used_time: z.number(),
  expired_time: z.number(),
  used_subscription_id: z.number(),
})

export type ResetCard = z.infer<typeof resetCardSchema>

/** Generic API response */
export interface ApiResponse<T = unknown> {
  success: boolean
  message?: string
  data?: T
}

export interface GetResetCardsParams {
  p?: number
  page_size?: number
}

export interface GetResetCardsResponse {
  success: boolean
  message?: string
  data?: {
    items: ResetCard[]
    total: number
    page: number
    page_size: number
  }
}

export interface SearchResetCardsParams extends GetResetCardsParams {
  keyword?: string
  status?: string
}

export interface GrantResetCardsPayload {
  user_id: number
  name: string
  count: number
  expired_time: number // 秒级 Unix 时间戳，0 = 不过期
}

export type ResetCardsDialogType = 'grant' | 'disable' | 'delete'
```

- [ ] **Step 2: api.ts**

```ts
import { api } from '@/lib/api'

import type {
  ApiResponse,
  GetResetCardsParams,
  GetResetCardsResponse,
  GrantResetCardsPayload,
  SearchResetCardsParams,
} from './types'

export async function getResetCards(
  params: GetResetCardsParams = {}
): Promise<GetResetCardsResponse> {
  const { p = 1, page_size = 10 } = params
  const res = await api.get(
    `/api/subscription/admin/reset_cards/?p=${p}&page_size=${page_size}`
  )
  return res.data
}

export async function searchResetCards(
  params: SearchResetCardsParams
): Promise<GetResetCardsResponse> {
  const { keyword = '', status = '', p = 1, page_size = 10 } = params
  const res = await api.get(
    `/api/subscription/admin/reset_cards/search?keyword=${encodeURIComponent(keyword)}&status=${encodeURIComponent(status)}&p=${p}&page_size=${page_size}`
  )
  return res.data
}

export async function grantResetCards(
  payload: GrantResetCardsPayload
): Promise<ApiResponse<null>> {
  const res = await api.post('/api/subscription/admin/reset_cards/grant', payload)
  return res.data
}

export async function disableResetCards(
  ids: number[]
): Promise<ApiResponse<number>> {
  const res = await api.post('/api/subscription/admin/reset_cards/disable', {
    ids,
  })
  return res.data
}

export async function deleteResetCard(id: number): Promise<ApiResponse<null>> {
  const res = await api.delete(`/api/subscription/admin/reset_cards/${id}`)
  return res.data
}
```

- [ ] **Step 3: constants.ts**

```ts
import type { TFunction } from 'i18next'

import type { StatusBadgeProps } from '@/components/status-badge'

// ============================================================================
// Reset Card Status Configuration
// ============================================================================

export const RESET_CARD_STATUS = {
  UNUSED: 1,
  USED: 2,
  DISABLED: 3,
} as const

// labelKey values are i18n keys; use t(config.labelKey) in components
export const RESET_CARD_STATUSES: Record<
  number,
  Pick<StatusBadgeProps, 'variant'> & {
    labelKey: string
    value: number
  }
> = {
  [RESET_CARD_STATUS.UNUSED]: {
    labelKey: 'Unused',
    variant: 'success',
    value: RESET_CARD_STATUS.UNUSED,
  },
  [RESET_CARD_STATUS.USED]: {
    labelKey: 'Used',
    variant: 'neutral',
    value: RESET_CARD_STATUS.USED,
  },
  [RESET_CARD_STATUS.DISABLED]: {
    labelKey: 'Disabled',
    variant: 'neutral',
    value: RESET_CARD_STATUS.DISABLED,
  },
} as const

// Virtual status filter value for expired cards
// Note: "Expired" is not a real DB status, it's computed from expired_time
export const RESET_CARD_FILTER_EXPIRED = 'expired'

export const RESET_CARD_FILTER_VALUES = [
  String(RESET_CARD_STATUS.UNUSED),
  String(RESET_CARD_STATUS.USED),
  String(RESET_CARD_STATUS.DISABLED),
  RESET_CARD_FILTER_EXPIRED,
] as const

export function getResetCardStatusOptions(t: TFunction) {
  return [
    ...Object.values(RESET_CARD_STATUSES).map((config) => ({
      label: t(config.labelKey),
      value: String(config.value),
    })),
    {
      label: t('Expired'),
      value: RESET_CARD_FILTER_EXPIRED,
    },
  ]
}

// ============================================================================
// Validation Constants
// ============================================================================

export const RESET_CARD_VALIDATION = {
  NAME_MIN_LENGTH: 1,
  NAME_MAX_LENGTH: 50,
  COUNT_MIN: 1,
  COUNT_MAX: 100,
} as const

// ============================================================================
// Error Messages (i18n keys; use t(ERROR_MESSAGES.xxx) when displaying)
// ============================================================================

export const ERROR_MESSAGES = {
  LOAD_FAILED: 'Failed to load reset cards',
  NAME_LENGTH_INVALID: 'Reset card name length must be between 1-50',
  COUNT_INVALID: 'Count must be between 1 and 100',
  USER_REQUIRED: 'Please select a user',
} as const

export function getResetCardFormErrorMessages(t: TFunction) {
  return {
    NAME_LENGTH_INVALID: t(ERROR_MESSAGES.NAME_LENGTH_INVALID),
    COUNT_INVALID: t(ERROR_MESSAGES.COUNT_INVALID),
    USER_REQUIRED: t(ERROR_MESSAGES.USER_REQUIRED),
  } as const
}

// ============================================================================
// Success Messages (i18n keys; use t(SUCCESS_MESSAGES.xxx) when displaying)
// ============================================================================

export const SUCCESS_MESSAGES = {
  RESET_CARDS_GRANTED: 'Reset cards granted successfully',
  RESET_CARD_DISABLED: 'Reset card disabled',
  RESET_CARD_DELETED: 'Reset card deleted',
} as const
```

- [ ] **Step 4: lib/utils.ts + lib/reset-card-form.ts + lib/index.ts**

`lib/utils.ts`：

```ts
import { RESET_CARD_STATUS } from '../constants'

/** timestamp 为秒级 Unix 时间戳；0 表示不过期 */
export function isTimestampExpired(timestamp: number): boolean {
  return timestamp !== 0 && timestamp * 1000 < Date.now()
}

export function isResetCardExpired(
  expiredTime: number,
  status: number
): boolean {
  return status === RESET_CARD_STATUS.UNUSED && isTimestampExpired(expiredTime)
}
```

`lib/reset-card-form.ts`：

```ts
import type { TFunction } from 'i18next'
import { z } from 'zod'

import {
  RESET_CARD_VALIDATION,
  getResetCardFormErrorMessages,
} from '../constants'
import type { GrantResetCardsPayload } from '../types'

export function getResetCardGrantFormSchema(t: TFunction) {
  const msg = getResetCardFormErrorMessages(t)
  return z.object({
    user_id: z.number().min(1, msg.USER_REQUIRED),
    name: z
      .string()
      .min(RESET_CARD_VALIDATION.NAME_MIN_LENGTH, msg.NAME_LENGTH_INVALID)
      .max(RESET_CARD_VALIDATION.NAME_MAX_LENGTH, msg.NAME_LENGTH_INVALID),
    count: z
      .number()
      .int()
      .min(RESET_CARD_VALIDATION.COUNT_MIN, msg.COUNT_INVALID)
      .max(RESET_CARD_VALIDATION.COUNT_MAX, msg.COUNT_INVALID),
    expired_time: z.date().optional(),
  })
}

export type ResetCardGrantFormValues = {
  user_id: number
  name: string
  count: number
  expired_time?: Date
}

export const RESET_CARD_GRANT_DEFAULT_VALUES: ResetCardGrantFormValues = {
  user_id: 0,
  name: '',
  count: 1,
  expired_time: undefined,
}

export function transformGrantFormToPayload(
  data: ResetCardGrantFormValues
): GrantResetCardsPayload {
  return {
    user_id: data.user_id,
    name: data.name,
    count: data.count,
    expired_time: data.expired_time
      ? Math.floor(data.expired_time.getTime() / 1000)
      : 0,
  }
}
```

`lib/index.ts`：

```ts
export { isResetCardExpired, isTimestampExpired } from './utils'
export {
  getResetCardGrantFormSchema,
  type ResetCardGrantFormValues,
  RESET_CARD_GRANT_DEFAULT_VALUES,
  transformGrantFormToPayload,
} from './reset-card-form'
```

- [ ] **Step 5: static-keys.ts 登记**

`web/src/i18n/static-keys.ts` 的 `STATIC_I18N_KEYS` 数组中（redemption 段附近）追加：

```ts
  // Reset cards
  'Reset cards granted successfully',
  'Reset card disabled',
  'Reset card deleted',
  'Failed to load reset cards',
  'Reset card name length must be between 1-50',
  'Count must be between 1 and 100',
  'Please select a user',
```

（'Unused' / 'Used' / 'Disabled' / 'Expired' 已在 redemption 段登记过则跳过，不重复添加。）

- [ ] **Step 6: 校验 + Commit**

```bash
cd web && bun run typecheck
bunx oxlint -c .oxlintrc.json src/features/reset-cards src/i18n/static-keys.ts
```

Expected: 无 error。然后：

```bash
git add web/src/features/reset-cards web/src/i18n/static-keys.ts
git commit -m "feat(web): add reset-cards feature foundations (types, api, constants, form lib)"
```

---

### Task 7: 发放对话框（TDD）

**Files:**
- Create: `web/src/features/reset-cards/components/reset-cards-grant-dialog.tsx`
- Test: `web/src/features/reset-cards/components/__tests__/reset-cards-grant-dialog.test.tsx`

**Interfaces:**
- Consumes: Task 6 的 `grantResetCards`、表单 lib、`SUCCESS_MESSAGES`；`@/features/users/api` 的 `searchUsers({ keyword, p, page_size })`（返回 `data.items: User[]`，`User` 有 `id`/`username`/`display_name`）；`@/components/ui/form`、`@/components/ui/sheet`、`@/components/drawer-layout` 的 `sideDrawerContentClassName` 等；`@/lib/handle-server-error`。
- Produces: `ResetCardsGrantDialog(props: { open: boolean; onOpenChange: (open: boolean) => void; onSuccess?: () => void })` — Task 8 页面接入。

- [ ] **Step 1: 写失败测试**

新建 `web/src/features/reset-cards/components/__tests__/reset-cards-grant-dialog.test.tsx`：

```tsx
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

const i18n = (await import('i18next')).default
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { Toaster, toast } = await import('sonner')
const { api } = await import('@/lib/api')
const { ResetCardsGrantDialog } = await import('../reset-cards-grant-dialog')

await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
  toast.dismiss()
})

function renderDialog(onOpenChange = () => undefined, onSuccess = () => undefined) {
  render(
    <I18nextProvider i18n={i18n}>
      <ResetCardsGrantDialog
        open
        onOpenChange={onOpenChange}
        onSuccess={onSuccess}
      />
      <Toaster duration={60_000} />
    </I18nextProvider>
  )
}

describe('reset card grant dialog', () => {
  test('rejects count below 1 and above 100 without calling the API', async () => {
    const postCalls: unknown[] = []
    apiClient.get = async () => ({ data: { success: true, data: { items: [], total: 0 } } })
    apiClient.post = async (url: string, data?: unknown) => {
      postCalls.push({ url, data })
      return { data: { success: true, data: null } }
    }
    renderDialog()

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: '补偿卡' } })
    const countInput = screen.getByLabelText(/count/i)

    fireEvent.change(countInput, { target: { value: '0' } })
    fireEvent.click(screen.getByRole('button', { name: /grant/i }))
    expect(await screen.findByText('Count must be between 1 and 100')).toBeInTheDocument()

    fireEvent.change(countInput, { target: { value: '101' } })
    fireEvent.click(screen.getByRole('button', { name: /grant/i }))
    expect(await screen.findAllByText('Count must be between 1 and 100')).not.toHaveLength(0)

    expect(postCalls).toHaveLength(0)
  })

  test('submits a valid grant, closes the dialog and calls onSuccess', async () => {
    const postCalls: { url: string; data?: unknown }[] = []
    apiClient.get = async (url: string) => {
      if (url.startsWith('/api/user/search')) {
        return {
          data: {
            success: true,
            data: {
              items: [{ id: 7, username: 'alice', display_name: 'Alice' }],
              total: 1,
              page: 1,
              page_size: 10,
            },
          },
        }
      }
      return { data: { success: false } }
    }
    apiClient.post = async (url: string, data?: unknown) => {
      postCalls.push({ url, data })
      return { data: { success: true, data: null } }
    }

    const onOpenChange = vi.fn()
    const onSuccess = vi.fn()
    renderDialog(onOpenChange, onSuccess)

    // 搜索并选择用户
    fireEvent.change(screen.getByPlaceholderText(/search user/i), {
      target: { value: 'alice' },
    })
    const userOption = await screen.findByRole('button', { name: /alice/i })
    fireEvent.click(userOption)

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: '9月补偿卡' } })
    fireEvent.change(screen.getByLabelText(/count/i), { target: { value: '2' } })
    fireEvent.click(screen.getByRole('button', { name: /grant/i }))

    await waitFor(() => {
      expect(postCalls).toHaveLength(1)
    })
    expect(postCalls[0].url).toBe('/api/subscription/admin/reset_cards/grant')
    expect(postCalls[0].data).toMatchObject({
      user_id: 7,
      name: '9月补偿卡',
      count: 2,
      expired_time: 0,
    })
    await waitFor(() => {
      expect(onOpenChange).toHaveBeenCalledWith(false)
      expect(onSuccess).toHaveBeenCalled()
    })
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && bun run test -- src/features/reset-cards/components/__tests__/reset-cards-grant-dialog.test.tsx`
Expected: FAIL（模块 `../reset-cards-grant-dialog` 不存在）。

- [ ] **Step 3: 实现发放对话框**

新建 `web/src/features/reset-cards/components/reset-cards-grant-dialog.tsx`（记得 AGPL 头）：

```tsx
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

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
import {
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
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
        const res = await searchUsers({ keyword: userKeyword, p: 1, page_size: 10 })
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
                      min={1}
                      max={100}
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
```

注意：`SheetContent`/`SheetHeader`/`SheetFooter`/`SheetTitle`/`SheetDescription` 是独立导出组件（见 `web/src/components/ui/sheet.tsx` 末尾 export 块与 `redemptions-mutate-drawer.tsx:265-282` 的实际用法），不要用点号子组件写法。`sideDrawer*ClassName` 来自 `@/components/drawer-layout`（drawer-layout.ts，注意是 `.ts` 不是 `.tsx`）。测试中的 label 定位依赖 `FormLabel` 与输入的关联——若项目 `FormLabel` 不自动关联控件，保留输入上的 `aria-label`（上面已加）保证 `getByLabelText` 命中。

- [ ] **Step 4: 跑测试确认通过**

Run: `cd web && bun run test -- src/features/reset-cards/components/__tests__/reset-cards-grant-dialog.test.tsx`
Expected: 2 个用例 PASS。若定位器失败，按实际渲染结果调整 `aria-label`/placeholder 后再跑，直到通过。

- [ ] **Step 5: typecheck + lint + Commit**

```bash
cd web && bun run typecheck
bunx oxlint -c .oxlintrc.json src/features/reset-cards
git add web/src/features/reset-cards
git commit -m "feat(web): add reset card grant dialog with user search"
```

---

### Task 8: 管理页表格、行操作、页面、路由、侧边栏

**Files:**
- Create: `web/src/features/reset-cards/components/reset-cards-provider.tsx`
- Create: `web/src/features/reset-cards/components/reset-cards-columns.tsx`
- Create: `web/src/features/reset-cards/components/data-table-row-actions.tsx`
- Create: `web/src/features/reset-cards/components/reset-cards-disable-dialog.tsx`
- Create: `web/src/features/reset-cards/components/reset-cards-delete-dialog.tsx`
- Create: `web/src/features/reset-cards/components/reset-cards-dialogs.tsx`
- Create: `web/src/features/reset-cards/components/reset-cards-primary-buttons.tsx`
- Create: `web/src/features/reset-cards/components/reset-cards-table.tsx`
- Create: `web/src/features/reset-cards/components/reset-cards-mobile-list.tsx`
- Create: `web/src/features/reset-cards/index.tsx`
- Create: `web/src/routes/_authenticated/reset-cards/index.tsx`
- Modify: `web/src/hooks/use-sidebar-data.ts`（admin 组，Redemption Codes 条目之后）

**Interfaces:**
- Consumes: Task 6 全部导出；Task 7 的 `ResetCardsGrantDialog`；`@/components/data-table`（`DataTablePage`、`useDataTable`、`DataTableColumnHeader`）；`@/components/confirm-dialog`（`ConfirmDialog`，props：`{open, onOpenChange, title, desc, handleConfirm, isLoading, destructive?, confirmText?, className?}`）；`@/components/status-badge`；`@/components/layout` 的 `SectionPageLayout`；`@/hooks/use-table-url-state`；`@/hooks` 的 `useMediaQuery`；`@/hooks/use-dialog`；`@/lib/roles` 的 `ROLE.ADMIN`；`@/stores/auth-store`。
- Produces: 路由 `/_authenticated/reset-cards/`（页面组件 `ResetCards`）；侧边栏条目 `url: '/reset-cards'`。

- [ ] **Step 1: provider + dialogs + primary buttons**

`components/reset-cards-provider.tsx`（逐字镜像 `redemption-codes/components/redemptions-provider.tsx`，替换类型名）：

```tsx
import React, { useState } from 'react'

import useDialogState from '@/hooks/use-dialog'

import { type ResetCard, type ResetCardsDialogType } from '../types'

type ResetCardsContextType = {
  open: ResetCardsDialogType | null
  setOpen: (str: ResetCardsDialogType | null) => void
  currentRow: ResetCard | null
  setCurrentRow: React.Dispatch<React.SetStateAction<ResetCard | null>>
  refreshTrigger: number
  triggerRefresh: () => void
}

const ResetCardsContext = React.createContext<ResetCardsContextType | null>(null)

export function ResetCardsProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useDialogState<ResetCardsDialogType>(null)
  const [currentRow, setCurrentRow] = useState<ResetCard | null>(null)
  const [refreshTrigger, setRefreshTrigger] = useState(0)

  const triggerRefresh = () => setRefreshTrigger((prev) => prev + 1)

  return (
    <ResetCardsContext
      value={{ open, setOpen, currentRow, setCurrentRow, refreshTrigger, triggerRefresh }}
    >
      {children}
    </ResetCardsContext>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export const useResetCards = () => {
  const context = React.useContext(ResetCardsContext)
  if (!context) {
    throw new Error('useResetCards has to be used within <ResetCardsProvider>')
  }
  return context
}
```

`components/reset-cards-primary-buttons.tsx`：

```tsx
import { Plus } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { useResetCards } from './reset-cards-provider'

export function ResetCardsPrimaryButtons() {
  const { t } = useTranslation()
  const { setOpen } = useResetCards()
  return (
    <Button onClick={() => setOpen('grant')}>
      <Plus className='mr-2 size-4' aria-hidden='true' />
      {t('Grant Reset Cards')}
    </Button>
  )
}
```

`components/reset-cards-disable-dialog.tsx`：

```tsx
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
      desc={t('This will disable the reset card. The user will no longer be able to use it.')}
      confirmText={t('Disable')}
    />
  )
}
```

`components/reset-cards-delete-dialog.tsx`：同上结构，`deleteResetCard(currentRow.id)`，标题 `t('Delete Reset Card?')`，desc `t('This will permanently delete the reset card. This action cannot be undone.')`，confirmText `t('Delete')`，成功 toast `t(SUCCESS_MESSAGES.RESET_CARD_DELETED)`。

`components/reset-cards-dialogs.tsx`：

```tsx
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
```

- [ ] **Step 2: columns + row actions**

`components/reset-cards-columns.tsx`：

```tsx
import type { ColumnDef } from '@tanstack/react-table'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'

import { DataTableColumnHeader } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'

import { RESET_CARD_STATUSES } from '../constants'
import { isResetCardExpired } from '../lib'
import type { ResetCard } from '../types'
import { DataTableRowActions } from './data-table-row-actions'

function formatTimestamp(ts: number): string {
  return ts === 0 ? '-' : dayjs.unix(ts).format('YYYY-MM-DD HH:mm')
}

export function useResetCardsColumns(): ColumnDef<ResetCard>[] {
  const { t } = useTranslation()
  return [
    {
      accessorKey: 'id',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title='ID' />
      ),
      cell: ({ row }) => <span className='tabular-nums'>{row.original.id}</span>,
      meta: { mobileHidden: true },
    },
    {
      accessorKey: 'name',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Name')} />
      ),
      cell: ({ row }) => (
        <span className='font-medium'>{row.original.name}</span>
      ),
      meta: { mobileTitle: true },
    },
    {
      accessorKey: 'user_id',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('User ID')} />
      ),
      cell: ({ row }) => (
        <span className='tabular-nums'>{row.original.user_id}</span>
      ),
    },
    {
      id: 'status',
      header: () => t('Status'),
      cell: ({ row }) => {
        const card = row.original
        if (isResetCardExpired(card.expired_time, card.status)) {
          return (
            <StatusBadge label={t('Expired')} variant='warning' copyable={false} />
          )
        }
        const config = RESET_CARD_STATUSES[card.status]
        if (!config) return null
        return (
          <StatusBadge
            label={t(config.labelKey)}
            variant={config.variant}
            copyable={false}
          />
        )
      },
      meta: { mobileBadge: true },
    },
    {
      accessorKey: 'created_time',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Created At')} />
      ),
      cell: ({ row }) => formatTimestamp(row.original.created_time),
      meta: { mobileHidden: true },
    },
    {
      accessorKey: 'expired_time',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Expiration')} />
      ),
      cell: ({ row }) =>
        row.original.expired_time === 0
          ? t('Never')
          : formatTimestamp(row.original.expired_time),
      meta: { mobileHidden: true },
    },
    {
      accessorKey: 'used_time',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Used At')} />
      ),
      cell: ({ row }) => formatTimestamp(row.original.used_time),
      meta: { mobileHidden: true },
    },
    {
      accessorKey: 'used_subscription_id',
      header: ({ column }) => (
        <DataTableColumnHeader column={column} title={t('Used Subscription')} />
      ),
      cell: ({ row }) =>
        row.original.used_subscription_id === 0
          ? '-'
          : `#${row.original.used_subscription_id}`,
      meta: { mobileHidden: true },
    },
    {
      id: 'actions',
      header: () => t('Actions'),
      cell: ({ row }) => <DataTableRowActions row={row} />,
      meta: { pinned: 'right' as const },
    },
  ]
}
```

`components/data-table-row-actions.tsx`：

```tsx
import type { Row } from '@tanstack/react-table'
import { Ban, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { DataTableRowActionMenu } from '@/components/data-table/core/row-action-menu'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'

import { RESET_CARD_STATUS } from '../constants'
import { isResetCardExpired } from '../lib'
import { resetCardSchema } from '../types'
import { useResetCards } from './reset-cards-provider'

interface DataTableRowActionsProps<TData> {
  row: Row<TData>
}

export function DataTableRowActions<TData>({
  row,
}: DataTableRowActionsProps<TData>) {
  const { t } = useTranslation()
  const card = resetCardSchema.parse(row.original)
  const { setOpen, setCurrentRow } = useResetCards()
  const canDisable =
    card.status === RESET_CARD_STATUS.UNUSED &&
    !isResetCardExpired(card.expired_time, card.status)

  return (
    <DataTableRowActionMenu>
      {canDisable && (
        <DropdownMenuItem
          onClick={() => {
            setCurrentRow(card)
            setOpen('disable')
          }}
        >
          {t('Disable')}
          <Ban className='ml-auto size-4' aria-hidden='true' />
        </DropdownMenuItem>
      )}
      <DropdownMenuSeparator />
      <DropdownMenuItem
        onClick={() => {
          setCurrentRow(card)
          setOpen('delete')
        }}
      >
        {t('Delete')}
        <Trash2 className='ml-auto size-4' aria-hidden='true' />
      </DropdownMenuItem>
    </DataTableRowActionMenu>
  )
}
```

（`DataTableRowActionMenu` 的 children/菜单项写法以 `web/src/features/redemption-codes/components/data-table-row-actions.tsx` 实际结构为准——若菜单项图标用 `DropdownMenuShortcut` 包裹则对齐之。）

- [ ] **Step 3: 表格 + 移动端列表 + 页面**

`components/reset-cards-table.tsx`（镜像 `redemptions-table.tsx`，去掉行选择与 bulkActions）：

```tsx
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTablePage, useDataTable } from '@/components/data-table'
import { useMediaQuery } from '@/hooks'
import { useTableUrlState } from '@/hooks/use-table-url-state'

import { getResetCards, searchResetCards } from '../api'
import { ERROR_MESSAGES, getResetCardStatusOptions } from '../constants'
import { ResetCardsMobileList } from './reset-cards-mobile-list'
import { useResetCardsColumns } from './reset-cards-columns'
import { useResetCards } from './reset-cards-provider'

const route = getRouteApi('/_authenticated/reset-cards/')

export function ResetCardsTable() {
  const { t } = useTranslation()
  const isMobile = useMediaQuery('(max-width: 768px)')
  const { refreshTrigger } = useResetCards()
  const columns = useResetCardsColumns()
  const statusOptions = getResetCardStatusOptions(t)

  const {
    globalFilter,
    onGlobalFilterChange,
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
    ensurePageInRange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: { defaultPage: 1, defaultPageSize: isMobile ? 10 : 20 },
    globalFilter: { enabled: true, key: 'filter' },
    columnFilters: [{ columnId: 'status', searchKey: 'status', type: 'array' }],
  })
  const statusFilter =
    (columnFilters.find((filter) => filter.id === 'status')?.value as
      | string[]
      | undefined) ?? []
  const statusFilterValue = statusFilter[0] ?? ''

  const { data, isLoading, isFetching } = useQuery({
    queryKey: [
      'reset-cards',
      pagination.pageIndex + 1,
      pagination.pageSize,
      globalFilter,
      statusFilterValue,
      refreshTrigger,
    ],
    queryFn: async () => {
      const hasFilter = globalFilter?.trim()
      const hasStatusFilter = statusFilterValue !== ''
      const params = {
        p: pagination.pageIndex + 1,
        page_size: pagination.pageSize,
      }
      const result =
        hasFilter || hasStatusFilter
          ? await searchResetCards({
              ...params,
              keyword: globalFilter,
              status: statusFilterValue,
            })
          : await getResetCards(params)

      if (!result.success) {
        toast.error(t(ERROR_MESSAGES.LOAD_FAILED))
        return { items: [], total: 0 }
      }
      return {
        items: result.data?.items || [],
        total: result.data?.total || 0,
      }
    },
    placeholderData: (previousData) => previousData,
  })

  const { table } = useDataTable({
    data: data?.items || [],
    columns,
    getRowId: (row) => String(row.id),
    columnFilters,
    globalFilter,
    pagination,
    onPaginationChange,
    onGlobalFilterChange,
    onColumnFiltersChange,
    manualPagination: true,
    manualFiltering: true,
    totalCount: data?.total || 0,
    ensurePageInRange,
  })

  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={isLoading}
      isFetching={isFetching}
      emptyTitle={t('No Reset Cards Found')}
      emptyDescription={t(
        'No reset cards available. Grant your first reset card to get started.'
      )}
      skeletonKeyPrefix='reset-cards-skeleton'
      applyHeaderSize
      toolbarProps={{
        searchPlaceholder: t('Filter by name, ID or user ID...'),
        searchDebounceMs: 500,
        filters: [
          {
            columnId: 'status',
            title: t('Status'),
            options: statusOptions,
            singleSelect: true,
          },
        ],
      }}
      mobile={<ResetCardsMobileList table={table} isLoading={isLoading} />}
    />
  )
}
```

`components/reset-cards-mobile-list.tsx`：镜像 `redemptions-mobile-list.tsx`（逐字读该文件后仿写），每行显示卡名称、状态徽标（过期虚拟态优先）、`ID: {id} / User: {user_id}`、创建时间与 `DataTableRowActions`；空态标题 `t('No Reset Cards Found')`、描述同上。

`index.tsx`：

```tsx
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
```

- [ ] **Step 4: 路由 + 侧边栏**

新建 `web/src/routes/_authenticated/reset-cards/index.tsx`：

```tsx
import { createFileRoute, redirect } from '@tanstack/react-router'
import z from 'zod'

import { ResetCards } from '@/features/reset-cards'
import { RESET_CARD_FILTER_VALUES } from '@/features/reset-cards/constants'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

const resetCardsSearchSchema = z.object({
  page: z.number().optional().catch(1),
  pageSize: z.number().optional().catch(10),
  filter: z.string().optional().catch(''),
  status: z.array(z.enum(RESET_CARD_FILTER_VALUES)).optional().catch([]),
})

export const Route = createFileRoute('/_authenticated/reset-cards/')({
  beforeLoad: () => {
    const { auth } = useAuthStore.getState()
    if (!auth.user || auth.user.role < ROLE.ADMIN) {
      throw redirect({ to: '/403' })
    }
  },
  validateSearch: resetCardsSearchSchema,
  component: ResetCards,
})
```

`web/src/hooks/use-sidebar-data.ts`：admin 组中 Redemption Codes 条目之后插入（`TicketCheck` 加进 lucide-react import）：

```ts
          {
            title: t('Reset Cards'),
            url: '/reset-cards',
            icon: TicketCheck,
          },
```

- [ ] **Step 5: 重建路由树 + 校验**

```bash
cd web && bun run build
```

Expected: TanStack Router 插件重新生成 `src/routeTree.gen.ts`（含 reset-cards 路由），构建成功。随后：

```bash
bun run typecheck
bunx oxlint -c .oxlintrc.json src/features/reset-cards src/routes/_authenticated/reset-cards src/hooks/use-sidebar-data.ts
```

Expected: 无 error。

- [ ] **Step 6: Commit**

```bash
git add web/src/features/reset-cards web/src/routes/_authenticated/reset-cards web/src/hooks/use-sidebar-data.ts web/src/routeTree.gen.ts
git commit -m "feat(web): add reset cards admin page with table, row actions, route and sidebar entry"
```

---

### Task 9: 钱包页"使用重置卡"按钮（TDD）

**Files:**
- Modify: `web/src/features/subscriptions/api.ts`（追加两个函数）
- Create: `web/src/features/wallet/components/reset-card-use-button.tsx`
- Modify: `web/src/features/wallet/components/subscription-plans-card.tsx`（每个有效订阅行的操作区）
- Test: `web/src/features/wallet/components/__tests__/reset-card-use-button.test.tsx`

**Interfaces:**
- Consumes: 后端 Task 5 的用户端契约（`GET /api/subscription/self/reset_cards` → `data.count`；`POST /api/subscription/self/reset_cards/use` body `{subscription_id}`）；`@/components/confirm-dialog`；`@/components/ui/button`；`@/lib/handle-server-error`；`features/subscriptions/api.ts` 的 `ApiResponse` 类型。
- Produces:
  - `getSelfResetCardCount(): Promise<ApiResponse<{ count: number }>>`、`useSubscriptionResetCard(subscriptionId: number): Promise<ApiResponse<{ card_id: number; subscription_id: number }>>`（挂在 `@/features/subscriptions/api`）
  - `ResetCardUseButton(props: { subscriptionId: number; onUsed: () => void })`
  - React Query key 约定：`['reset-cards', 'self-count']`

- [ ] **Step 1: subscriptions/api.ts 追加**

```ts
export async function getSelfResetCardCount(): Promise<
  ApiResponse<{ count: number }>
> {
  const res = await api.get('/api/subscription/self/reset_cards')
  return res.data
}

export async function useSubscriptionResetCard(
  subscriptionId: number
): Promise<ApiResponse<{ card_id: number; subscription_id: number }>> {
  const res = await api.post('/api/subscription/self/reset_cards/use', {
    subscription_id: subscriptionId,
  })
  return res.data
}
```

- [ ] **Step 2: 写失败测试**

新建 `web/src/features/wallet/components/__tests__/reset-card-use-button.test.tsx`：

```tsx
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

const i18n = (await import('i18next')).default
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } = await import('@tanstack/react-query')
const { Toaster, toast } = await import('sonner')
const { api } = await import('@/lib/api')
const { ResetCardUseButton } = await import('../reset-card-use-button')

await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod; post: ApiMethod }

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
  toast.dismiss()
})

function renderButton(onUsed = () => undefined) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <ResetCardUseButton subscriptionId={9201} onUsed={onUsed} />
        <Toaster duration={60_000} />
      </I18nextProvider>
    </QueryClientProvider>
  )
}

describe('reset card use button', () => {
  test('shows remaining count and stays enabled when cards are available', async () => {
    apiClient.get = async () => ({ data: { success: true, data: { count: 2 } } })
    renderButton()
    const button = await screen.findByRole('button', {
      name: /use reset card.*2/i,
    })
    expect(button).toBeEnabled()
  })

  test('disables the button when no cards are available', async () => {
    apiClient.get = async () => ({ data: { success: true, data: { count: 0 } } })
    renderButton()
    const button = await screen.findByRole('button', {
      name: /use reset card/i,
    })
    expect(button).toBeDisabled()
  })

  test('confirming the dialog calls the use API and refreshes via onUsed', async () => {
    const postCalls: { url: string; data?: unknown }[] = []
    let count = 1
    apiClient.get = async () => ({ data: { success: true, data: { count } } })
    apiClient.post = async (url: string, data?: unknown) => {
      postCalls.push({ url, data })
      count = 0
      return { data: { success: true, data: { card_id: 5, subscription_id: 9201 } } }
    }
    const onUsed = vi.fn()
    renderButton(onUsed)

    const button = await screen.findByRole('button', { name: /use reset card/i })
    fireEvent.click(button)

    // 确认框出现，说明文案解释清零语义
    expect(await screen.findByText(/clear the used quota/i)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /^use reset card$/i }))

    await waitFor(() => {
      expect(postCalls).toHaveLength(1)
    })
    expect(postCalls[0].url).toBe('/api/subscription/self/reset_cards/use')
    expect(postCalls[0].data).toEqual({ subscription_id: 9201 })
    await waitFor(() => {
      expect(onUsed).toHaveBeenCalled()
    })
  })
})
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd web && bun run test -- src/features/wallet/components/__tests__/reset-card-use-button.test.tsx`
Expected: FAIL（`../reset-card-use-button` 不存在）。

- [ ] **Step 4: 实现按钮组件**

新建 `web/src/features/wallet/components/reset-card-use-button.tsx`（AGPL 头）：

```tsx
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { TicketCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import {
  getSelfResetCardCount,
  useSubscriptionResetCard,
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
    mutationFn: () => useSubscriptionResetCard(props.subscriptionId),
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
```

- [ ] **Step 5: 跑测试确认通过**

Run: `cd web && bun run test -- src/features/wallet/components/__tests__/reset-card-use-button.test.tsx`
Expected: 3 个用例 PASS（必要时按实际渲染微调正则定位器，直到通过）。

- [ ] **Step 6: 集成进订阅卡片**

`web/src/features/wallet/components/subscription-plans-card.tsx`：
1. import 区追加 `import { ResetCardUseButton } from './reset-card-use-button'`；
2. 在 `allSubscriptions.map(...)` 渲染块内（每个订阅的 `<div className='bg-background rounded-md border p-3 text-xs'>` 容器中），`<Progress ... />` 元素之后加一行操作区，仅对有效订阅渲染：

```tsx
                {isActive && subscription?.id && (
                  <div className='mt-2 flex justify-end'>
                    <ResetCardUseButton
                      subscriptionId={subscription.id}
                      onUsed={fetchSelfSubscription}
                    />
                  </div>
                )}
```

（该文件用 `fetchSelfSubscription` 回调刷新订阅数据，正好接到 `onUsed`；`isActive` 与 `subscription` 变量沿用该 map 块内已有命名，若实际命名不同以现有代码为准。）

- [ ] **Step 7: 校验 + Commit**

```bash
cd web && bun run typecheck
bunx oxlint -c .oxlintrc.json src/features/wallet src/features/subscriptions/api.ts
bun run test -- src/features/wallet/components/__tests__/reset-card-use-button.test.tsx
```

Expected: 全部通过。

```bash
git add web/src/features/subscriptions/api.ts web/src/features/wallet
git commit -m "feat(web): add use-reset-card button to wallet subscription card"
```

---

### Task 10: 前端 i18n locale 补全

**Files:**
- Modify: `web/src/i18n/locales/en.json`
- Modify: `web/src/i18n/locales/zh.json`
- （其余语言由 sync 脚本补齐）

- [ ] **Step 1: en.json 追加 key**（`translation` 对象内，identity 映射；若 key 已存在则跳过）

```
"Reset Cards": "Reset Cards"
"Grant Reset Cards": "Grant Reset Cards"
"Grant subscription reset cards directly to a user.": "Grant subscription reset cards directly to a user."
"Grant": "Grant"
"Granting...": "Granting..."
"User": "User"
"Search user by username...": "Search user by username..."
"Change": "Change"
"Name": "Name"
"e.g. September compensation card": "e.g. September compensation card"
"Count": "Count"
"1-100 cards per grant": "1-100 cards per grant"
"Expiration Time": "Expiration Time"
"Leave empty for no expiration": "Leave empty for no expiration"
"Filter by name, ID or user ID...": "Filter by name, ID or user ID..."
"No Reset Cards Found": "No Reset Cards Found"
"No reset cards available. Grant your first reset card to get started.": "No reset cards available. Grant your first reset card to get started."
"Created At": "Created At"
"Expiration": "Expiration"
"Never": "Never"
"Used At": "Used At"
"Used Subscription": "Used Subscription"
"Disable Reset Card?": "Disable Reset Card?"
"This will disable the reset card. The user will no longer be able to use it.": "This will disable the reset card. The user will no longer be able to use it."
"Delete Reset Card?": "Delete Reset Card?"
"This will permanently delete the reset card. This action cannot be undone.": "This will permanently delete the reset card. This action cannot be undone."
"Reset cards granted successfully": "Reset cards granted successfully"
"Reset card disabled": "Reset card disabled"
"Reset card deleted": "Reset card deleted"
"Failed to load reset cards": "Failed to load reset cards"
"Reset card name length must be between 1-50": "Reset card name length must be between 1-50"
"Count must be between 1 and 100": "Count must be between 1 and 100"
"Please select a user": "Please select a user"
"Use reset card ({{count}} left)": "Use reset card ({{count}} left)"
"Use subscription reset card?": "Use subscription reset card?"
"This will clear the used quota of this subscription for the current period and restart the reset cycle.": "This will clear the used quota of this subscription for the current period and restart the reset cycle."
"Subscription quota reset successfully": "Subscription quota reset successfully"
```

（'Unused'/'Used'/'Disabled'/'Expired'/'Status'/'Actions'/'Disable'/'Delete'/'User ID' 等通用 key 已存在，确认即可，不重复添加。）

- [ ] **Step 2: zh.json 追加对应中文翻译**

```
"Reset Cards": "重置卡"
"Grant Reset Cards": "发放重置卡"
"Grant subscription reset cards directly to a user.": "直接向用户发放订阅重置卡。"
"Grant": "发放"
"Granting...": "发放中..."
"User": "用户"
"Search user by username...": "按用户名搜索用户..."
"Change": "更换"
"e.g. September compensation card": "例如：9月补偿卡"
"Count": "数量"
"1-100 cards per grant": "每次发放 1-100 张"
"Expiration Time": "过期时间"
"Leave empty for no expiration": "留空表示不过期"
"Filter by name, ID or user ID...": "按名称、卡 ID 或用户 ID 筛选..."
"No Reset Cards Found": "未找到重置卡"
"No reset cards available. Grant your first reset card to get started.": "暂无重置卡，点击发放按钮创建第一张。"
"Expiration": "过期时间"
"Never": "永不"
"Used At": "使用时间"
"Used Subscription": "使用的订阅"
"Disable Reset Card?": "禁用重置卡？"
"This will disable the reset card. The user will no longer be able to use it.": "禁用后该用户将无法再使用此卡。"
"Delete Reset Card?": "删除重置卡？"
"This will permanently delete the reset card. This action cannot be undone.": "将永久删除该重置卡，此操作无法撤销。"
"Reset cards granted successfully": "重置卡发放成功"
"Reset card disabled": "重置卡已禁用"
"Reset card deleted": "重置卡已删除"
"Failed to load reset cards": "加载重置卡失败"
"Reset card name length must be between 1-50": "重置卡名称长度必须在 1-50 之间"
"Count must be between 1 and 100": "数量必须在 1-100 之间"
"Please select a user": "请选择用户"
"Use reset card ({{count}} left)": "使用重置卡（剩 {{count}} 张）"
"Use subscription reset card?": "使用订阅重置卡？"
"This will clear the used quota of this subscription for the current period and restart the reset cycle.": "将清零该订阅当前周期已用额度并重新开始重置周期。"
"Subscription quota reset successfully": "订阅额度已重置"
```

- [ ] **Step 3: 同步其余语言并校验**

```bash
cd web && bun run i18n:sync
bun run typecheck
```

Expected: 脚本把新 key 传播到 fr/ja/ru/vi/zh-TW（fallback 为英文），无报错。

- [ ] **Step 4: Commit**

```bash
git add web/src/i18n/locales
git commit -m "feat(web): add reset card i18n strings"
```
