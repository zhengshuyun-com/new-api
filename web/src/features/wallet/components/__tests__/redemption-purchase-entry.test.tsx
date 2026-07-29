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
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'
import type React from 'react'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'HTMLAnchorElement',
  'HTMLButtonElement',
  'HTMLInputElement',
  'SVGElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { RechargeFormCard } = await import('../recharge-form-card')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: {
    en: {
      translation: {
        'Get one here': 'Get one here',
        Redeem: 'Redeem',
      },
    },
  },
})

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

type RenderedRechargeForm = {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}

const baseProps: React.ComponentProps<typeof RechargeFormCard> = {
  topupInfo: {
    enable_online_topup: false,
    enable_stripe_topup: false,
    pay_methods: [],
    min_topup: 1,
    stripe_min_topup: 1,
    amount_options: [],
    discount: {},
    enable_redemption: true,
  },
  presetAmounts: [],
  selectedPreset: null,
  onSelectPreset: () => {},
  topupAmount: 0,
  onTopupAmountChange: () => {},
  paymentAmount: 0,
  calculating: false,
  onPaymentMethodSelect: () => {},
  paymentLoading: null,
  redemptionCode: '',
  onRedemptionCodeChange: () => {},
  onRedeem: () => {},
  redeeming: false,
}

async function renderRechargeForm(
  props: Partial<React.ComponentProps<typeof RechargeFormCard>> = {}
): Promise<RenderedRechargeForm> {
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)

  await act(async () => {
    root.render(
      <I18nextProvider i18n={i18n}>
        <RechargeFormCard {...baseProps} {...props} />
      </I18nextProvider>
    )
  })

  return { container, root }
}

async function unmountRechargeForm(rendered: RenderedRechargeForm) {
  await act(async () => rendered.root.unmount())
  rendered.container.remove()
}

describe('redemption purchase entry', () => {
  after(() => {
    domWindow.close()
  })

  test('shows the configured purchase link as an independent button', async () => {
    const rendered = await renderRechargeForm({
      topupLink: 'https://shop.example.com/redemption',
    })

    const purchaseLink = rendered.container.querySelector<HTMLAnchorElement>(
      'a[href="https://shop.example.com/redemption"]'
    )
    assert.ok(purchaseLink)
    assert.equal(purchaseLink.textContent?.includes('Get one here'), true)
    assert.equal(purchaseLink.dataset.slot, 'button')
    assert.equal(purchaseLink.target, '_blank')
    assert.equal(purchaseLink.rel, 'noopener noreferrer')

    const redeemButton = [
      ...rendered.container.querySelectorAll('button'),
    ].find((button) => button.textContent?.includes('Redeem'))
    assert.ok(redeemButton)
    assert.equal(redeemButton.parentElement, purchaseLink.parentElement)

    await unmountRechargeForm(rendered)
  })

  test('does not show a purchase button when no purchase link is configured', async () => {
    const rendered = await renderRechargeForm()

    assert.equal(
      rendered.container.querySelector('a[data-slot="button"]'),
      null
    )

    await unmountRechargeForm(rendered)
  })
})
