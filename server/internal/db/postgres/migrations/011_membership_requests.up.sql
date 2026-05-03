-- Membership requests: a confirmed user without org membership can ask to
-- join an org by slug; org admins approve or reject. UNIQUE (org,user)
-- means re-requests update the existing row in place (state machine).

CREATE TABLE membership_requests (
  uid              uuid PRIMARY KEY,
  organization_uid uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  user_uid         uuid NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
  message          text,
  status           text NOT NULL CHECK (status IN ('pending','approved','rejected','canceled')),
  decision_reason  text,
  decided_at       timestamptz,
  decided_by_uid   uuid REFERENCES users(uid) ON DELETE SET NULL,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (organization_uid, user_uid)
);

CREATE INDEX membership_requests_org_status_idx
  ON membership_requests (organization_uid, status);

CREATE INDEX membership_requests_user_status_idx
  ON membership_requests (user_uid, status);
