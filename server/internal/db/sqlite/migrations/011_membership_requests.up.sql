CREATE TABLE membership_requests (
  uid              text PRIMARY KEY,
  organization_uid text NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  user_uid         text NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
  message          text,
  status           text NOT NULL CHECK (status IN ('pending','approved','rejected','canceled')),
  decision_reason  text,
  decided_at       text,
  decided_by_uid   text REFERENCES users(uid) ON DELETE SET NULL,
  created_at       text NOT NULL DEFAULT (datetime('now')),
  updated_at       text NOT NULL DEFAULT (datetime('now')),
  UNIQUE (organization_uid, user_uid)
);

CREATE INDEX membership_requests_org_status_idx
  ON membership_requests (organization_uid, status);

CREATE INDEX membership_requests_user_status_idx
  ON membership_requests (user_uid, status);
