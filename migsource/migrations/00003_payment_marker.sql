-- +goose NO TRANSACTION
-- +goose Up

-- Payment lifecycle + system-message infrastructure.
--
-- Background: passengers tap Pay on the post-trip card, the app
-- fires a UPI deeplink, and we optimistically record
-- bookings.dismissal_signal='paid'. The host never sees this — they
-- have no surface telling them who has and hasn't paid. Bi-confirm
-- via the ride group chat is the canonical signal: the passenger's
-- Pay tap posts a special "payment_marker" message into the ride
-- chat, and the host taps Confirm Received / Didn't Receive inline.
-- That handler flips bookings.payment_status and posts a follow-up
-- system message so everyone in the chat sees the outcome.
--
-- Schema additions:
--   bookings.payment_status         enum-ish string. Defaults to 'none'
--                                    (no payment expected/pending);
--                                    flips to 'pending' when the
--                                    passenger marks paid, then to
--                                    'confirmed' or 'disputed' when
--                                    the host acks.
--   bookings.payment_confirmed_at   stamp of the host's ack.
--   messages.kind                   defaults to 'user'. Other values
--                                    are 'payment_marker' (passenger
--                                    declared paid) and 'payment_ack'
--                                    (host confirmed / disputed).
--                                    Clients render non-'user' messages
--                                    as system cards instead of text
--                                    bubbles.
--   messages.metadata               jsonb sidecar for the renderer —
--                                    payment_marker carries
--                                    {booking_id, amount, passenger_name};
--                                    payment_ack carries
--                                    {booking_id, ack: 'received'|'missing'}.
--
-- All non-trivial defaults are inline so existing rows backfill
-- cleanly without a separate UPDATE pass.

-- +goose StatementBegin
ALTER TABLE bookings
    ADD COLUMN IF NOT EXISTS payment_status STRING NOT NULL DEFAULT 'none';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE bookings
    ADD COLUMN IF NOT EXISTS payment_confirmed_at TIMESTAMPTZ NULL;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS kind STRING NOT NULL DEFAULT 'user';
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
ALTER TABLE messages DROP COLUMN IF EXISTS metadata;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE messages DROP COLUMN IF EXISTS kind;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE bookings DROP COLUMN IF EXISTS payment_confirmed_at;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE bookings DROP COLUMN IF EXISTS payment_status;
-- +goose StatementEnd
