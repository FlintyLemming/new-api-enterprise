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

import { CopyButton } from '@/components/copy-button'

import { SettingsControlGroup } from '../components/settings-form-layout'

const EXCHANGE_KEY_REQUEST_HEADER = 'Authorization: Bearer sk-<username>-<hmac>'

const EXCHANGE_KEY_HMAC_FORMULA = [
  'hmac = hex(HMAC-SHA256(secret, username))',
  'key  = sk-<username>-<hmac>',
].join('\n')

export function ExchangeKeyUsageGuide() {
  const { t } = useTranslation()

  return (
    <SettingsControlGroup className='space-y-3'>
      <h4 className='text-sm font-medium'>{t('How to call')}</h4>
      <p className='text-muted-foreground text-sm leading-6'>
        {t(
          'Internal apps keep the shared secret locally, compute HMAC-SHA256 of the username, and send the result as the API key:'
        )}
      </p>
      <CodeSnippet
        value={EXCHANGE_KEY_REQUEST_HEADER}
        copyLabel={t('Copy request header')}
      />
      <p className='text-muted-foreground text-sm leading-6'>
        {t(
          'The same value is also accepted as Anthropic x-api-key, Gemini key / x-goog-api-key, and WebSocket openai-insecure-api-key.'
        )}
      </p>
      <p className='text-muted-foreground text-sm leading-6'>
        {t(
          'Compute the HMAC as follows. The message is the username UTF-8 bytes and does not include sk- or the suffix:'
        )}
      </p>
      <CodeSnippet
        value={EXCHANGE_KEY_HMAC_FORMULA}
        copyLabel={t('Copy HMAC formula')}
      />
      <ul className='text-muted-foreground list-disc space-y-1.5 pl-5 text-sm leading-6'>
        <li>
          {t(
            'The username must match the existing account exactly (case-sensitive, hyphens allowed, 1-20 characters).'
          )}
        </li>
        <li>{t('The user must already exist and not be banned.')}</li>
        <li>
          {t(
            "Charges use that user's wallet or subscription. No token row is created."
          )}
        </li>
        <li>
          {t('Usage logs show these calls under the token name exchange-key.')}
        </li>
        <li>
          {t(
            'Do not append a channel ID. After rotating the secret, every app must recompute the HMAC.'
          )}
        </li>
      </ul>
    </SettingsControlGroup>
  )
}

function CodeSnippet(props: { value: string; copyLabel: string }) {
  return (
    <div className='flex min-w-0 items-start gap-2'>
      <pre className='bg-muted text-foreground min-w-0 flex-1 overflow-x-auto rounded-md px-3 py-2 font-mono text-xs leading-relaxed'>
        {props.value}
      </pre>
      <CopyButton
        value={props.value}
        size='icon'
        className='size-7'
        tooltip={props.copyLabel}
        aria-label={props.copyLabel}
      />
    </div>
  )
}
