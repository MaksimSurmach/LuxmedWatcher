package storage

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id %[4]s,
	telegram_user_id %[5]s NOT NULL UNIQUE,
	telegram_chat_id %[5]s NOT NULL,
	telegram_username TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	locale TEXT NOT NULL,
	credential_revision BIGINT NOT NULL DEFAULT 0,
	preferred_city_id INTEGER,
	preferred_city_name TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	role TEXT NOT NULL,
	created_at %[6]s NOT NULL,
	updated_at %[6]s NOT NULL,
	last_seen_at %[6]s NOT NULL
);

CREATE TABLE IF NOT EXISTS invite_codes (
	id %[4]s,
	code_hash TEXT NOT NULL UNIQUE,
	created_by_user_id %[5]s NOT NULL,
	max_uses INTEGER NOT NULL,
	used_count INTEGER NOT NULL DEFAULT 0,
	expires_at %[6]s,
	disabled_at %[6]s,
	created_at %[6]s NOT NULL
);

CREATE TABLE IF NOT EXISTS luxmed_accounts (
	id %[4]s,
	user_id %[5]s NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
	login TEXT NOT NULL,
	encrypted_password TEXT NOT NULL,
	use_saved_login BOOLEAN NOT NULL DEFAULT FALSE,
	encrypted_session_data TEXT NOT NULL DEFAULT '',
	session_expires_at %[6]s,
	last_login_at %[6]s,
	last_login_error TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS watches (
	id %[4]s,
	user_id %[5]s NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('%[1]s','%[2]s','%[3]s')),
	city_id INTEGER NOT NULL,
	city_name TEXT NOT NULL,
	service_id INTEGER NOT NULL,
	service_name TEXT NOT NULL,
	doctor_id INTEGER,
	doctor_name TEXT NOT NULL DEFAULT '',
	doctor_mode TEXT NOT NULL,
	facility_mode TEXT NOT NULL,
	facility_ids TEXT NOT NULL DEFAULT '[]',
	facility_names TEXT NOT NULL DEFAULT '[]',
	date_from %[6]s,
	date_to %[6]s,
	next_days INTEGER NOT NULL DEFAULT 14,
	time_windows TEXT NOT NULL DEFAULT '[]',
	check_interval_seconds INTEGER NOT NULL,
	last_checked_at %[6]s,
	last_success_at %[6]s,
	last_error TEXT NOT NULL DEFAULT '',
	created_at %[6]s NOT NULL,
	updated_at %[6]s NOT NULL
);

CREATE TABLE IF NOT EXISTS appointment_history (
	id %[4]s,
	watch_id %[5]s NOT NULL REFERENCES watches(id) ON DELETE CASCADE,
	external_id TEXT NOT NULL DEFAULT '',
	fingerprint TEXT NOT NULL,
	date_time %[6]s NOT NULL,
	service_id INTEGER NOT NULL,
	service_name TEXT NOT NULL,
	doctor_id INTEGER NOT NULL DEFAULT 0,
	doctor_name TEXT NOT NULL DEFAULT '',
	facility_id INTEGER NOT NULL DEFAULT 0,
	facility_name TEXT NOT NULL DEFAULT '',
	address TEXT NOT NULL DEFAULT '',
	city_id INTEGER NOT NULL DEFAULT 0,
	city_name TEXT NOT NULL DEFAULT '',
	booking_url TEXT NOT NULL DEFAULT '',
	first_seen_at %[6]s NOT NULL,
	last_seen_at %[6]s NOT NULL,
	last_notified_at %[6]s,
	seen_count INTEGER NOT NULL DEFAULT 1,
	status TEXT NOT NULL,
	raw_payload TEXT NOT NULL DEFAULT '',
	UNIQUE(watch_id, fingerprint)
);

CREATE TABLE IF NOT EXISTS notification_history (
	id %[4]s,
	watch_id %[5]s NOT NULL REFERENCES watches(id) ON DELETE CASCADE,
	appointment_history_id %[5]s NOT NULL REFERENCES appointment_history(id) ON DELETE CASCADE,
	appointment_fingerprint TEXT NOT NULL,
	sent_at %[6]s NOT NULL,
	message_id INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS luxmed_cities (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	updated_at %[6]s NOT NULL
);

CREATE TABLE IF NOT EXISTS luxmed_procedures (
	id INTEGER NOT NULL,
	city_id INTEGER NOT NULL,
	city_name TEXT NOT NULL,
	name TEXT NOT NULL,
	is_recent BOOLEAN NOT NULL DEFAULT FALSE,
	updated_at %[6]s NOT NULL,
	PRIMARY KEY (id, city_id)
);

CREATE TABLE IF NOT EXISTS luxmed_facilities (
	id INTEGER NOT NULL,
	city_id INTEGER NOT NULL,
	city_name TEXT NOT NULL,
	procedure_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	address TEXT NOT NULL DEFAULT '',
	updated_at %[6]s NOT NULL,
	PRIMARY KEY (id, city_id, procedure_id)
);

CREATE TABLE IF NOT EXISTS favorite_facilities (
	user_id %[5]s NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	city_id INTEGER NOT NULL,
	procedure_id INTEGER NOT NULL,
	facility_id INTEGER NOT NULL,
	facility_name TEXT NOT NULL,
	last_used_at %[6]s NOT NULL,
	PRIMARY KEY (user_id, city_id, procedure_id, facility_id)
);

CREATE INDEX IF NOT EXISTS idx_watches_due ON watches(status, last_checked_at);
CREATE INDEX IF NOT EXISTS idx_history_watch ON appointment_history(watch_id, date_time);
CREATE INDEX IF NOT EXISTS idx_notifications_fingerprint ON notification_history(watch_id, appointment_fingerprint, sent_at);
CREATE INDEX IF NOT EXISTS idx_luxmed_procedures_search ON luxmed_procedures(city_id, name);
CREATE INDEX IF NOT EXISTS idx_luxmed_facilities_lookup ON luxmed_facilities(city_id, procedure_id, name);
`
