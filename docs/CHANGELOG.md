# Changelog

## Conflicts, Help and operator tools (completed)

Work started in Antigravity and finished afterwards. What changed:

**Fixed**
- Conflict seeding queried a column that does not exist (`payout_items.purpose`) and wrote statuses straight into the database. It now produces all seven conflicts through the real flows.
- A reply that tried to give instructions no longer lost its escalation (`InboundReply` skipped re-evaluation).
- Snooze used an invalid interval expression; it now uses `make_interval`, checks the action exists and is still proposed.
- Operator notes were accepted for payouts that do not exist (now 404) and had no length limit.
- The audit CSV export could be abused with spreadsheet formulas (cells starting with `=`, `+`, `-`, `@` are now neutralised).
- The item search filter (`GET /api/items?q=`) produced invalid SQL.
- A test read a stale item and compared the wrong status.
- The pages Conflicts and Help were never wired into the app (no routes, no sidebar entries).

**Rewritten**
- Conflict detectors announce an event only when a conflict is really opened; resolutions validate the chosen email, refuse `refresh_now` while PayPal still disagrees, and re-evaluate afterwards.
- Benchmark scenarios H01 to H07 now assert that each conflict is detected (before, they only checked for no duplicate payments). The benchmark has 39 scenarios.
- Help page: generated from the API (demo payouts, rules matrix, routes), with search highlighting and deep links. The duplicated `demoData.ts` was deleted.

**Added**
- Command palette, `?` help drawer, keyboard shortcuts, first-run tour.
- Bulk acknowledge (messages only), snooze, notes, print report, audit CSV export, CSV import with preview, duplicate-payout confirmation, conflict column and banner.
- `GET /api/demo/payouts`, `POST /api/batches/check`, rules matrix in `GET /api/reference`.
- Tests: all seven conflict kinds through real flows, resolution rules, bulk-acknowledge refusing money-moving actions, snooze, notes, CSV safety, duplicate check, generated rules matrix.
