-- M8 operator chat: approval buttons. A chat message that proposes an
-- operator action is linked to its ai_actions row; the two button actions
-- join the single-use action-ref kinds.

ALTER TABLE chat_action_refs DROP CONSTRAINT IF EXISTS chat_action_refs_action_check;
ALTER TABLE chat_action_refs ADD CONSTRAINT chat_action_refs_action_check
  CHECK (action IN ('later', 'mute30d', 'ack', 'approve_action', 'decline_action'));

CREATE TABLE chat_approvals (
  -- No FK: the link is written before the message so a button can never
  -- exist without its approval.
  message_id uuid PRIMARY KEY,
  org_id uuid NOT NULL REFERENCES orgs(id),
  approval_id uuid NOT NULL REFERENCES ai_actions(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX chat_approvals_approval_idx ON chat_approvals (approval_id);

ALTER TABLE chat_approvals ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_approvals
  USING (org_id = nullif(current_setting('app.org_id', true), '')::uuid)
  WITH CHECK (org_id = nullif(current_setting('app.org_id', true), '')::uuid);
GRANT SELECT ON chat_approvals TO haetae_app;
