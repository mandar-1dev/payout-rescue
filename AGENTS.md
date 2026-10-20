# Rules for anyone (or any coding agent) extending PayoutRescue

Read `ARCHITECTURE.md` first. These rules protect the project's one promise: it never pays someone twice and never moves money without a named human approval.

## Never break
1. Do not add any code path that creates, cancels or re-sends a payout without an `approved` action and a recorded approver.
2. Keep the partial unique index `one_live_item_per_payout`. Never relax it.
3. A replacement may only follow a fresh PayPal readback that shows FAILED, RETURNED or CANCELED.
4. Retries must reuse the same `PayPal-Request-Id` and `sender_batch_id`. Replacements derive new ids deterministically.
5. The AI module must never import `internal/paypal` or `internal/store`, and must never supply amounts or currencies.
6. The server must refuse any PayPal base URL other than the sandbox.
7. Never log secrets. Never store real personal data; use synthetic recipients.
8. Audit every state change and every action. `audit_log` is append-only.

## Before you finish any change
- `make test` passes (Go with `-race`, then `tsc` and `eslint`). The randomised never-pay-twice test must stay green.
- Add a benchmark scenario in `internal/bench/bench.go` for any new failure mode, and label it `FAKE`, `SIMULATED` or `LIVE_SANDBOX` honestly.
- Update `ARCHITECTURE.md` if you change an invariant or the state machine.
- Mark anything you could not confirm against PayPal's docs with `TODO(verify)`.

## Good extensions
CSV batch import; recipient self-service claim page; Slack or email notifications (real sending must be a new, approval-gated action); multi-currency suggestions entered by an operator; role-based approvals; a read-only MCP wrapper over status queries.

## Style
Go: standard library first, small packages, table-driven tests. Frontend: keep the design tokens in `web/src/index.css`, never rely on colour alone for status, keep AG Grid on the Community edition.
