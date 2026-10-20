export function money(amount: string | number, currency: string): string {
  const n = typeof amount === 'string' ? parseFloat(amount) : amount
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(n)
  } catch {
    return `${n.toFixed(2)} ${currency}`
  }
}

export function when(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function plural(n: number, one: string, many = one + 's'): string {
  return `${n} ${n === 1 ? one : many}`
}

/** Plain-language explanations for PayPal's documented payout error names. */
const explanations: Record<string, string> = {
  INVALID_EMAIL: "The email on file isn't a valid PayPal email.",
  INVALID_EMAIL_ID: "The email on file isn't a valid PayPal email.",
  RECEIVER_UNREGISTERED: 'The recipient has no PayPal account for this email.',
  RECEIVER_UNCONFIRMED: "The recipient's PayPal account is not confirmed.",
  RECEIVER_ACCOUNT_LOCKED: "The recipient's PayPal account is locked.",
  RECEIVER_ACCOUNT_LIMITATION: "The recipient's account has a limitation.",
  UNVERIFIED_RECIPIENT_NOT_SUPPORTED: 'The recipient must verify their account first.',
  CURRENCY_NOT_SUPPORTED_FOR_RECEIVER: "The recipient's account can't receive this currency.",
  REFUSED_RECEIVER_REFUSED: 'The recipient refused the payment.',
  RECEIVING_LIMIT_EXCEEDED: "The recipient's account reached a receiving limit.",
  RECEIVER_COUNTRY_NOT_ALLOWED: 'PayPal does not allow this payout to that country.',
  REGULATORY_BLOCKED: 'Blocked for regulatory reasons.',
  RISK_DECLINE: 'Declined by PayPal risk checks.',
  DUPLICATE_ITEM: 'PayPal reports a duplicate item in the batch.',
}

export function explain(status: string, code: string): string {
  if (code && explanations[code]) return explanations[code]
  if (code) return `PayPal reported ${code}.`
  switch (status) {
    case 'UNCLAIMED': return 'Waiting for the recipient to claim it.'
    case 'ONHOLD': return 'PayPal is holding this payment.'
    case 'BLOCKED': return 'PayPal blocked this payment.'
    case 'SUCCESS': return 'Paid.'
    case 'RETURNED': return 'PayPal returned the unclaimed money.'
    case 'CANCELED': return 'Cancelled and returned to you.'
    case 'PENDING': case 'CREATED': return 'In progress.'
    default: return ''
  }
}

export const actionLabel: Record<string, string> = {
  notify: 'Message to recipient',
  cancel: 'Cancel unclaimed payout',
  reissue: 'Send one replacement',
  escalate: 'Needs a human decision',
  flag: 'For your information',
}
