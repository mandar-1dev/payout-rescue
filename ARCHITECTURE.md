# Architecture

One Go service: HTTP API, webhook receiver, reconciler and the embedded React app. PostgreSQL is the only datastore.

```mermaid
flowchart LR
  UI[React app] -- /api, SSE --> API[httpapi]
  PP[(PayPal sandbox)] -- webhook --> API
  API --> ENG[engine]
  ENG --> RULES[rules + state]
  ENG --> AI[ai: templates, extraction]
  ENG --> CL[paypal client]
  CL --> PP
  ENG --> DB[(PostgreSQL)]
  REC[reconciler] --> ENG
```

## Item state machine

```mermaid
stateDiagram-v2
  [*] --> CREATED
  CREATED --> PENDING
  CREATED --> UNCLAIMED
  CREATED --> SUCCESS
  CREATED --> FAILED
  PENDING --> SUCCESS
  PENDING --> FAILED
  PENDING --> UNCLAIMED
  UNCLAIMED --> SUCCESS: recipient claims
  UNCLAIMED --> CANCELED: approved cancel, confirmed by readback
  UNCLAIMED --> RETURNED: PayPal returns at day 30
  PENDING --> ONHOLD
  ONHOLD --> SUCCESS
  PENDING --> BLOCKED
  SUCCESS --> REFUNDED
  SUCCESS --> REVERSED
```

`FAILED`, `RETURNED` and `CANCELED` are the only states from which a replacement may be created ("reissue-safe"). A readback that asks for an impossible transition is ignored, flagged for review and audited.

## Invariants (enforced in code, covered by tests)

1. **At most one live item per logical payout.** A partial unique index (`one_live_item_per_payout`) makes a second one impossible even if application code is wrong.
2. **A replacement follows a confirmed-dead original.** The original is read back from PayPal at approval time; webhooks and cached state are never trusted for this.
3. **Replacements use deterministic ids** (`sender_batch_id`, `sender_item_id`, `PayPal-Request-Id` derived from the payout and attempt number). A crash or lost response is retried with the same ids, so PayPal replays instead of creating a second payout.
4. **No money-moving action runs without a recorded, named approval.** Cancel and replace are the only money-moving kinds.
5. **Webhook handling is idempotent.** Events are stored once by id; the item is read back before state changes.
6. **AI never decides.** It drafts text and extracts an email, a currency, a refusal and an injection flag. It cannot approve, call PayPal, or supply an amount or currency.
7. **Fail closed.** An inconclusive readback changes nothing and flags the item.

## Replace flow (the dangerous one)

1. Operator approves. The approval is recorded and audited.
2. Fresh readback of the original; apply through the state machine.
3. Re-check: original is FAILED, RETURNED or CANCELED; attempt limit; corrected email valid.
4. One transaction reserves the replacement batch and item. The unique index rejects it if any live item exists.
5. Send with the deterministic request id. A 5xx or lost response leaves the action `approved`; the reconciler resumes it with the same ids.
6. Read the replacement back; mark the action executed.

## Failure modes

| Failure | Behaviour |
|---|---|
| Webhook duplicated, delayed, reordered, or missing | Stored once; readback wins over payload; reconciler recovers missing events |
| PayPal 5xx or timeout on create | Same ids retried; no second payout |
| Response lost after PayPal created the batch | Retry replays the same batch |
| Recipient claims during a cancel approval | Readback shows SUCCESS; cancel refused; no replacement |
| Reply contains instructions or an amount | Blocked by a deterministic guard before any model call; item flagged |
| Model unavailable or returns bad JSON | Deterministic extraction and templates |
| PayPal reports an unknown error code | Escalated, never retried |

## Data model

`logical_payouts` (the obligation) -> `payout_items` (each send attempt, `attempt`, `original_item_id`) -> `payout_batches`. `webhook_events` (unique event id), `actions` (proposed, approved, executed, rejected, failed; unique idempotency key), `messages`, `audit_log` (append-only, hash-chained, protected by a trigger), `bench_runs`.

## Modes

Every batch and item is labelled `LIVE_SANDBOX` (real sandbox), `SIMULATED` (PayPal's canned test-value responses, which do not change real state) or `FAKE` (in-memory stand-in). The label appears in the UI and in the benchmark and is never mixed silently.
