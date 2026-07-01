-- +goose Up

-- Dedup lease for the "wants to ride with you" invite email a UniPool user
-- sends to an external ride host (Contact -> Let them know you want in).
--
-- One invite per (inviter, external ride). The row insert IS the lease: the
-- handler INSERTs ON CONFLICT DO NOTHING, and RowsAffected == 1 means this
-- request owns the send. external_ride_id is the source's document id (TEXT,
-- not a local ride) so it has no foreign key; the inviter does.

CREATE TABLE IF NOT EXISTS external_invites_sent (
  inviter_user_id  UUID        NOT NULL,
  external_ride_id TEXT        NOT NULL,
  sent_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (inviter_user_id, external_ride_id),
  CONSTRAINT fk_external_invites_user
    FOREIGN KEY (inviter_user_id) REFERENCES users(id) ON DELETE CASCADE
);

-- +goose Down

DROP TABLE IF EXISTS external_invites_sent;
