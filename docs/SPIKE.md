# Day-1 spike (60 to 90 minutes): confirm what the sandbox really does

Run this before trusting the Live and Simulated modes. Record results in the README's "not yet verified" list.

1. `make check` prints a PayPal token (credentials and sandbox base URL are right).
2. In the app: **Batches > Live sandbox**, two recipients with emails that have no PayPal account. After a few seconds both should show **Unclaimed**. If they show Paid or Failed, note it; use Fake mode for the unclaimed lifecycle and say so in the demo.
3. **Batches > Simulated**, one recipient per test value. Each should read back as Failed with the named error. If a value returns a different shape or status, adjust `paypal.SimItem` and the `SimTags` table.
4. Register the webhook, create another live batch, and confirm an event appears in **Audit log** with `verified: true`.
5. Approve a cancel on a live unclaimed item. Confirm PayPal reports `CANCELED` (or `RETURNED`) on readback.

Go / no-go: steps 1 and 2 must work. If 3 or 4 fail, keep the corresponding scenarios labelled FAKE and say so.
