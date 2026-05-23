-- +goose Up
-- +goose StatementBegin

-- Baseline migration: snapshot of the schema as it existed in production
-- before goose took over. On any database where the `users` table already
-- exists, this whole batch is a no-op (every CREATE uses IF NOT EXISTS
-- and every constraint add is wrapped to tolerate prior existence) so
-- recording version 00001 against an established DB is safe.
--
-- On a fresh database (local dev, CI, staging from scratch) this builds
-- the entire schema in one shot.

CREATE TABLE IF NOT EXISTS institutes (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	name VARCHAR(200) NOT NULL,
	country VARCHAR(80) NULL,
	CONSTRAINT institutes_pkey PRIMARY KEY (id ASC),
	INDEX idx_institutes_deleted_at (deleted_at ASC)
);

CREATE TABLE IF NOT EXISTS institute_domains (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	domain VARCHAR(120) NOT NULL,
	institute_id UUID NOT NULL,
	CONSTRAINT institute_domains_pkey PRIMARY KEY (id ASC),
	INDEX idx_institute_domains_institute_id (institute_id ASC),
	UNIQUE INDEX idx_institute_domains_domain (domain ASC),
	INDEX idx_institute_domains_deleted_at (deleted_at ASC)
);

CREATE TABLE IF NOT EXISTS users (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	name VARCHAR(100) NOT NULL,
	email VARCHAR(100) NOT NULL,
	profile_picture_url STRING NULL,
	contact_number VARCHAR(20) NOT NULL,
	gender VARCHAR(10) NULL,
	yob INT8 NULL,
	default_address VARCHAR(255) NULL,
	fcm_token VARCHAR(500) NULL,
	platform VARCHAR(20) NULL,
	device_id VARCHAR(255) NULL,
	institute_id UUID NULL,
	is_email_verified BOOL NULL DEFAULT false,
	institute_email VARCHAR(120) NULL,
	upi_vpa VARCHAR(120) NULL,
	CONSTRAINT users_pkey PRIMARY KEY (id ASC),
	INDEX idx_users_deleted_at (deleted_at ASC),
	INDEX idx_users_is_email_verified (is_email_verified ASC),
	INDEX idx_users_institute_id (institute_id ASC),
	INDEX idx_users_email (email ASC),
	INDEX idx_users_id (id ASC)
);

CREATE TABLE IF NOT EXISTS rides (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	host_user_id UUID NOT NULL,
	start_location VARCHAR(255) NOT NULL,
	end_location VARCHAR(255) NOT NULL,
	start_latitude DECIMAL(10,8) NULL,
	start_longitude DECIMAL(11,8) NULL,
	end_latitude DECIMAL(10,8) NULL,
	end_longitude DECIMAL(11,8) NULL,
	start_time TIMESTAMPTZ NOT NULL,
	total_seats INT8 NOT NULL,
	booked_seats INT8 NOT NULL,
	total_price INT8 NOT NULL,
	is_ongoing INT8 NOT NULL DEFAULT 0,
	is_same_gender INT8 NOT NULL DEFAULT 0,
	settings JSONB NULL DEFAULT '{}',
	vehicle_info VARCHAR(200) NULL,
	CONSTRAINT rides_pkey PRIMARY KEY (id ASC),
	INDEX idx_rides_deleted_at (deleted_at ASC),
	INDEX idx_rides_host_user_id (host_user_id ASC),
	INDEX idx_rides_start_time (start_time ASC),
	INDEX idx_rides_is_ongoing (is_ongoing ASC),
	INDEX idx_rides_start_lat_lng (start_latitude ASC, start_longitude ASC) WHERE (start_latitude IS NOT NULL) AND (start_longitude IS NOT NULL),
	INVERTED INDEX idx_rides_start_geog ((st_setsrid(st_makepoint(start_longitude::FLOAT8, start_latitude::FLOAT8), 4326)::GEOGRAPHY)) WHERE (start_latitude IS NOT NULL) AND (start_longitude IS NOT NULL),
	INDEX idx_rides_open_upcoming (start_time ASC, host_user_id ASC) WHERE (deleted_at IS NULL) AND (is_ongoing = 0),
	INDEX idx_rides_host_time (host_user_id ASC, start_time ASC)
);

CREATE TABLE IF NOT EXISTS bookings (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	ride_id UUID NOT NULL,
	passenger_id UUID NOT NULL,
	request_status VARCHAR(10) NOT NULL,
	dismissed_at TIMESTAMPTZ NULL,
	dismissal_signal VARCHAR(20) NULL DEFAULT '',
	CONSTRAINT bookings_pkey PRIMARY KEY (id ASC),
	INDEX idx_bookings_dismissed_at (dismissed_at ASC),
	UNIQUE INDEX idx_booking_ride_passenger (ride_id ASC, passenger_id ASC),
	INDEX idx_bookings_deleted_at (deleted_at ASC),
	INDEX idx_bookings_ride_id (ride_id ASC),
	INDEX idx_bookings_passenger_id (passenger_id ASC),
	INDEX idx_bookings_request_status (request_status ASC),
	INDEX idx_bookings_passenger_status (passenger_id ASC, request_status ASC) WHERE deleted_at IS NULL,
	INDEX idx_bookings_ride_status (ride_id ASC, request_status ASC) WHERE deleted_at IS NULL,
	INDEX idx_bookings_ride_passenger (ride_id ASC, passenger_id ASC)
);

CREATE TABLE IF NOT EXISTS user_metadata (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	user_id UUID NOT NULL,
	fcm_token STRING NULL,
	CONSTRAINT user_metadata_pkey PRIMARY KEY (id ASC),
	INDEX idx_user_metadata_deleted_at (deleted_at ASC)
);

CREATE TABLE IF NOT EXISTS messages (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	ride_id STRING NULL,
	dm_room_id STRING NULL,
	sender_id UUID NOT NULL,
	content STRING NOT NULL,
	CONSTRAINT messages_pkey PRIMARY KEY (id ASC),
	INDEX idx_messages_dm_room_id (dm_room_id ASC),
	INDEX idx_messages_ride_id (ride_id ASC),
	INDEX idx_messages_deleted_at (deleted_at ASC),
	INDEX idx_messages_ride_created (ride_id ASC, created_at DESC),
	INDEX idx_messages_dm_created (dm_room_id ASC, created_at DESC)
);

CREATE TABLE IF NOT EXISTS chat_reads (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	user_id STRING NOT NULL,
	ride_id STRING NULL,
	dm_room_id STRING NULL,
	last_read_at TIMESTAMPTZ NOT NULL,
	CONSTRAINT chat_reads_pkey PRIMARY KEY (id ASC),
	UNIQUE INDEX idx_chat_reads_user_ride (user_id ASC, ride_id ASC),
	INDEX idx_chat_reads_deleted_at (deleted_at ASC),
	UNIQUE INDEX idx_chat_reads_user_dm (dm_room_id ASC),
	INDEX idx_chat_reads_dm_room_id (dm_room_id ASC)
);

CREATE TABLE IF NOT EXISTS reports (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	reporter_id UUID NOT NULL,
	reported_user_id UUID NULL,
	ride_id UUID NULL,
	chat_room_id VARCHAR(128) NULL,
	reason VARCHAR(40) NOT NULL,
	details STRING NULL,
	status VARCHAR(20) NOT NULL DEFAULT 'pending',
	CONSTRAINT reports_pkey PRIMARY KEY (id ASC),
	INDEX idx_reports_ride_id (ride_id ASC),
	INDEX idx_reports_reported_user_id (reported_user_id ASC),
	INDEX idx_reports_reporter_id (reporter_id ASC),
	INDEX idx_reports_deleted_at (deleted_at ASC),
	INDEX idx_reports_chat_room_id (chat_room_id ASC)
);

CREATE TABLE IF NOT EXISTS email_verifications (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	user_id UUID NOT NULL,
	email VARCHAR(120) NOT NULL,
	code_hash VARCHAR(64) NOT NULL,
	salt VARCHAR(32) NOT NULL,
	token VARCHAR(64) NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	attempts INT8 NULL DEFAULT 0,
	consumed_at TIMESTAMPTZ NULL,
	CONSTRAINT email_verifications_pkey PRIMARY KEY (id ASC),
	INDEX idx_email_verifications_user_id (user_id ASC),
	INDEX idx_email_verifications_deleted_at (deleted_at ASC),
	INDEX idx_email_verifications_expires_at (expires_at ASC),
	UNIQUE INDEX idx_email_verifications_token (token ASC),
	INDEX idx_email_verifications_email (email ASC)
);

CREATE TABLE IF NOT EXISTS notification_preferences (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	user_id UUID NOT NULL,
	category VARCHAR(40) NOT NULL,
	ride_id UUID NULL,
	enabled BOOL NOT NULL,
	CONSTRAINT notification_preferences_pkey PRIMARY KEY (id ASC),
	UNIQUE INDEX idx_notif_pref_ride (user_id ASC, category ASC, ride_id ASC) WHERE ride_id IS NOT NULL,
	UNIQUE INDEX idx_notif_pref_global (user_id ASC, category ASC) WHERE ride_id IS NULL,
	INDEX idx_notif_pref_lookup (user_id ASC, category ASC, ride_id ASC),
	INDEX idx_notification_preferences_deleted_at (deleted_at ASC)
);

CREATE TABLE IF NOT EXISTS ride_ratings (
	id UUID NOT NULL DEFAULT gen_random_uuid(),
	created_at TIMESTAMPTZ NULL,
	updated_at TIMESTAMPTZ NULL,
	deleted_at TIMESTAMPTZ NULL,
	ride_id UUID NOT NULL,
	rater_user_id UUID NOT NULL,
	rated_user_id UUID NOT NULL,
	stars INT8 NOT NULL,
	comment VARCHAR(240) NULL,
	CONSTRAINT ride_ratings_pkey PRIMARY KEY (id ASC),
	INDEX idx_ratings_ride (ride_id ASC),
	INDEX idx_ride_ratings_deleted_at (deleted_at ASC),
	INDEX idx_ratings_rated (rated_user_id ASC),
	INDEX idx_ratings_rater (rater_user_id ASC),
	UNIQUE INDEX idx_ratings_ride_pair (ride_id ASC, rater_user_id ASC, rated_user_id ASC),
	INDEX idx_ratings_rater_ride_rated (rater_user_id ASC, ride_id ASC, rated_user_id ASC),
	INDEX idx_ratings_ride_rater (ride_id ASC, rater_user_id ASC)
);

-- +goose StatementEnd

-- Foreign keys live outside the StatementBegin/End block so each ALTER is
-- run as its own statement. They are not wrapped in IF NOT EXISTS (CRDB
-- doesn't support that on ADD CONSTRAINT); on the production DB they
-- already exist, so this migration is recorded as applied by the bootstrap
-- without ever running these. On a fresh DB the constraints are added.

-- +goose StatementBegin
ALTER TABLE institute_domains ADD CONSTRAINT fk_institute_domains_institute FOREIGN KEY (institute_id) REFERENCES institutes(id);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE users ADD CONSTRAINT fk_users_institute FOREIGN KEY (institute_id) REFERENCES institutes(id);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE rides ADD CONSTRAINT fk_rides_host_user FOREIGN KEY (host_user_id) REFERENCES users(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE bookings ADD CONSTRAINT fk_bookings_ride FOREIGN KEY (ride_id) REFERENCES rides(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE bookings ADD CONSTRAINT fk_bookings_passenger FOREIGN KEY (passenger_id) REFERENCES users(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE user_metadata ADD CONSTRAINT fk_user_metadata_user FOREIGN KEY (user_id) REFERENCES users(id);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE messages ADD CONSTRAINT fk_messages_sender FOREIGN KEY (sender_id) REFERENCES users(id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE reports ADD CONSTRAINT fk_reports_reporter FOREIGN KEY (reporter_id) REFERENCES users(id);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE reports ADD CONSTRAINT fk_reports_reported_user FOREIGN KEY (reported_user_id) REFERENCES users(id);
-- +goose StatementEnd


-- +goose Down
-- Baseline migrations have no down. Tearing down the entire schema is
-- not something goose should be doing on demand; if you really need to
-- start over, drop the database and re-run from scratch.
SELECT 1;
