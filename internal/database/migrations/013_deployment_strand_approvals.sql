-- A deployment that would activate a new revision while the automation has
-- pending approvals is refused, because an approval can only be answered on
-- the revision that opened it. This records the operator's explicit decision
-- to activate anyway and leave those approvals unanswerable.
ALTER TABLE deployments ADD COLUMN IF NOT EXISTS strand_approvals BOOLEAN NOT NULL DEFAULT false;
