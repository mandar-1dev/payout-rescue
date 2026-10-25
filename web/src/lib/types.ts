export type Status =
  | 'CREATED' | 'PENDING' | 'SUCCESS' | 'UNCLAIMED' | 'FAILED' | 'ONHOLD' | 'BLOCKED'
  | 'RETURNED' | 'REFUNDED' | 'REVERSED' | 'CANCELED' | 'UNKNOWN'
export type Mode = 'LIVE_SANDBOX' | 'SIMULATED' | 'FAKE'

export interface Item {
  id: string
  logical_payout_id: string
  batch_id: string
  sender_item_id: string
  paypal_item_id: string
  recipient_email: string
  corrected_email: string
  attempt: number
  original_item_id: string
  status: Status
  error_code: string
  sim_tag: string
  mode: Mode
  needs_review: boolean
  amount: string
  currency: string
  recipient_name: string
  purpose: string
  created_at: string
  status_changed_at: string
  age_days: number
  days_left: number
  open_actions?: number
  open_conflicts?: number
}

export interface Conflict {
  id: string
  item_id: string
  kind: 'claim_race' | 'ambiguous_reply' | 'email_same_as_on_file' | 'refusal_vs_proposal' | 'held_with_correction' | 'attempts_exhausted' | 'status_disagreement' | string
  summary: string
  details: Record<string, unknown>
  status: 'open' | 'resolved'
  resolution?: string | null
  resolved_by?: string | null
  note?: string | null
  dedupe_key: string
  created_at: string
  resolved_at?: string | null
  item?: Item
}

export interface Note {
  id: string
  item_id: string
  author: string
  body: string
  created_at: string
}

export interface Action {
  id: string
  item_id: string
  kind: 'notify' | 'cancel' | 'reissue' | 'escalate' | 'flag'
  template_key: string
  reason: string
  status: 'proposed' | 'approved' | 'executed' | 'rejected' | 'failed'
  decided_by: string
  result: Record<string, unknown> | null
  created_at: string
  snoozed_until?: string | null
  item?: Item
  preflight?: Preflight
}

export interface Check { id: string; label: string; ok: boolean; detail: string }
export interface Preview {
  method: string; endpoint: string; sender_batch_id?: string; request_id?: string
  receiver?: string; amount?: string; currency?: string
}
export interface Preflight { action_id: string; kind: string; checks: Check[]; preview?: Preview; ready: boolean; note: string }

export interface Message {
  id: string; item_id: string; action_id: string; direction: 'outbound' | 'inbound'
  subject: string; body: string; status: string
  extracted: { new_email?: string; new_currency?: string; refuses?: boolean; unclear?: boolean; injection?: boolean; source?: string; notes?: string[]; candidates?: string[] } | null
  created_at: string
}

export interface AuditEntry { id: number; ts: string; actor: string; event: string; item_id: string; details: Record<string, unknown>; prev_hash: string; hash: string }

export interface ItemDetail {
  item: Item
  actions: Action[]
  messages: Message[]
  audit: AuditEntry[]
  preflight: Record<string, Preflight>
  attempts: Item[]
  conflicts?: Conflict[]
  notes?: Note[]
}

export interface Summary {
  in_limbo: { currency: string; amount: string }[] | null
  by_status: Record<string, number>
  by_mode: Record<string, number>
  buckets: { label: string; count: number; amount: string }[]
  needing_action: number
  next_return_days: number | null
  duplicates_prevented: number
  items: number
  webhook_events: number
  webhook_unprocessed: number
  audit_entries: number
  audit_intact: boolean
  webhook_bypass: boolean
  open_conflicts?: number
}

export interface DemoPayout {
  name: string
  email: string
  amount: string
  currency: string
  purpose: string
  group: string
  mode: Mode
  fake_outcome?: string
  story: string
  expected: string
  conflict_kind?: string
  try_this: string[]
}

export interface RouteDoc {
  pattern: string
  doc: string
}

export interface RuleProposal { Kind: string; TemplateKey: string; Reason: string }
export interface RuleRow { when: string; status: string; error_code: string; age_days: number; corrected_email: boolean; proposals: RuleProposal[] | null }
export interface DemoPayouts { standard: DemoPayout[]; conflicts: DemoPayout[] }
export interface DuplicateWarning { index: number; email: string; amount: string; currency: string; item_id: string; created_at: string }

export interface Reference {
  routes: RouteDoc[]
  statuses: Status[]
  action_kinds: string[]
  template_keys: string[]
  sim_tags: Record<string, string>
  conflict_resolutions: Record<string, string[]>
  rules_matrix: RuleRow[]
}

export interface Session {
  env: string; platform: string; live_configured: boolean; fake_enabled: boolean; llm: string
  webhook_bypass: boolean; webhook_url: string; webhook_id_set: boolean
  sim_tags: Record<string, string>; templates: string[]; reconcile_seconds: number
}

export interface BatchRow { id: string; sender_batch_id: string; paypal_batch_id: string; status: string; mode: Mode; created_at: string }

export interface Outcome { skipped: boolean; duplicate_payments: number; unsafe_retries: number; unnoticed_returns: number; manual_steps: number; paid_once: number; ok: boolean }
export interface ScenarioResult { id: string; group: string; name: string; label: Mode; naive: Outcome; payoutrescue: Outcome; pass: boolean; note?: string }
export interface BenchResult {
  run_at: string; seconds: number; disclaimer: string
  summary: {
    scenarios: number; passed: number
    naive_duplicate_payments: number; payoutrescue_duplicate_payments: number
    naive_unsafe_retries: number; payoutrescue_unsafe_retries: number
    naive_unnoticed_returns: number; payoutrescue_unnoticed_returns: number
    naive_manual_steps_assumed: number; payoutrescue_operator_steps: number
    extraction_correct: number; extraction_total: number; extraction_source: string
    label_counts: Record<string, number>; assumed_manual_steps_per_exception: number
    comparison_scenarios: number; payoutrescue_only_scenarios: number
  }
  scenarios: ScenarioResult[]
}
export interface BenchLatest { available: boolean; running: boolean; saved_at?: string; result?: BenchResult }
