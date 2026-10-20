CREATE TABLE logical_payouts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  purpose TEXT NOT NULL DEFAULT '',
  recipient_name TEXT NOT NULL,
  amount NUMERIC(12,2) NOT NULL CHECK (amount > 0),
  currency TEXT NOT NULL CHECK (char_length(currency) = 3),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE payout_batches (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  sender_batch_id TEXT NOT NULL UNIQUE,
  paypal_batch_id TEXT,
  request_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'CREATED',
  mode TEXT NOT NULL CHECK (mode IN ('LIVE_SANDBOX','SIMULATED','FAKE')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE payout_items (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  logical_payout_id UUID NOT NULL REFERENCES logical_payouts(id),
  batch_id UUID NOT NULL REFERENCES payout_batches(id),
  sender_item_id TEXT NOT NULL UNIQUE,
  paypal_item_id TEXT,
  recipient_email TEXT NOT NULL,
  corrected_email TEXT,
  attempt INT NOT NULL DEFAULT 1,
  original_item_id UUID REFERENCES payout_items(id),
  status TEXT NOT NULL,
  error_code TEXT,
  sim_tag TEXT,
  mode TEXT NOT NULL CHECK (mode IN ('LIVE_SANDBOX','SIMULATED','FAKE')),
  needs_review BOOLEAN NOT NULL DEFAULT false,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  status_changed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  version INT NOT NULL DEFAULT 1
);

-- Invariant (a), enforced by the database: at most one live item per logical payout.
CREATE UNIQUE INDEX one_live_item_per_payout ON payout_items (logical_payout_id)
  WHERE status IN ('CREATED','PENDING','UNCLAIMED','ONHOLD','BLOCKED','SUCCESS','UNKNOWN');
CREATE UNIQUE INDEX payout_items_paypal_item ON payout_items (paypal_item_id) WHERE paypal_item_id IS NOT NULL;
CREATE INDEX payout_items_status ON payout_items (status);
CREATE INDEX payout_items_batch ON payout_items (batch_id);

CREATE TABLE webhook_events (
  event_id TEXT PRIMARY KEY,
  event_type TEXT NOT NULL,
  resource_id TEXT NOT NULL DEFAULT '',
  verified BOOLEAN NOT NULL DEFAULT false,
  payload JSONB NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at TIMESTAMPTZ,
  error TEXT
);

CREATE TABLE actions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  item_id UUID NOT NULL REFERENCES payout_items(id),
  kind TEXT NOT NULL,
  template_key TEXT NOT NULL DEFAULT '',
  proposed_by TEXT NOT NULL DEFAULT 'rules',
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed','approved','executed','rejected','failed')),
  idempotency_key TEXT NOT NULL UNIQUE,
  payload JSONB NOT NULL DEFAULT '{}',
  decided_by TEXT,
  decided_at TIMESTAMPTZ,
  executed_at TIMESTAMPTZ,
  result JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX actions_status ON actions (status);
CREATE INDEX actions_item ON actions (item_id);

CREATE TABLE messages (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  item_id UUID NOT NULL REFERENCES payout_items(id),
  action_id UUID REFERENCES actions(id),
  direction TEXT NOT NULL CHECK (direction IN ('outbound','inbound')),
  subject TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'draft',
  extracted JSONB,
  flags JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX messages_item ON messages (item_id);

CREATE TABLE audit_log (
  id BIGSERIAL PRIMARY KEY,
  ts TIMESTAMPTZ NOT NULL,
  actor TEXT NOT NULL,
  event TEXT NOT NULL,
  item_id UUID,
  details JSONB NOT NULL DEFAULT '{}',
  prev_hash TEXT NOT NULL,
  hash TEXT NOT NULL
);
CREATE INDEX audit_item ON audit_log (item_id);

CREATE FUNCTION audit_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit_log is append-only';
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER audit_no_update BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION audit_immutable();

CREATE TABLE bench_runs (
  id BIGSERIAL PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  result JSONB NOT NULL
);
