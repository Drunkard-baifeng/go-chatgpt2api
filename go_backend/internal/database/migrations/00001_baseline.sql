-- PostgreSQL 基线：对应现有领域表，不改变领域所有权。

-- +goose Up

CREATE TABLE account_group_configuration (
	id SERIAL NOT NULL, 
	data JSONB NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE accounts (
	id SERIAL NOT NULL, 
	access_token TEXT NOT NULL, 
	data TEXT NOT NULL, 
	PRIMARY KEY (id)
);

CREATE UNIQUE INDEX ix_accounts_access_token_sha256 ON accounts (sha256(CAST(replace(access_token, chr(92), repeat(chr(92), 2)) AS BYTEA)));

CREATE TABLE application_coordination_lease (
	name VARCHAR(128) NOT NULL, 
	owner_token VARCHAR(64) NOT NULL, 
	expires_at FLOAT NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (name)
);

CREATE TABLE auth_keys (
	id SERIAL NOT NULL, 
	key_id VARCHAR(255) NOT NULL, 
	data TEXT NOT NULL, 
	PRIMARY KEY (id)
);

CREATE UNIQUE INDEX ix_auth_keys_key_id ON auth_keys (key_id);

CREATE TABLE backup_execution_state (
	id SERIAL NOT NULL, 
	data JSONB NOT NULL, 
	revision INTEGER NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE call_record_state (
	id SERIAL NOT NULL, 
	generation VARCHAR(64) NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE call_records (
	sequence SERIAL NOT NULL, 
	id VARCHAR(64) NOT NULL, 
	event_time VARCHAR(32) NOT NULL, 
	event_day VARCHAR(10) NOT NULL, 
	type VARCHAR(32) NOT NULL, 
	outcome VARCHAR(32) NOT NULL, 
	endpoint VARCHAR(255) NOT NULL, 
	model VARCHAR(255) NOT NULL, 
	account_email VARCHAR(320) NOT NULL, 
	conversation_id VARCHAR(255) NOT NULL, 
	business_kind VARCHAR(64) NOT NULL, 
	search_text TEXT NOT NULL, 
	payload JSON NOT NULL, 
	inserted_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (sequence), 
	UNIQUE (id)
);

CREATE INDEX ix_call_records_account_sequence ON call_records (account_email, sequence);

CREATE INDEX ix_call_records_conversation_sequence ON call_records (conversation_id, sequence);

CREATE INDEX ix_call_records_day_sequence ON call_records (event_day, sequence);

CREATE INDEX ix_call_records_endpoint_sequence ON call_records (endpoint, sequence);

CREATE INDEX ix_call_records_model_sequence ON call_records (model, sequence);

CREATE INDEX ix_call_records_outcome_sequence ON call_records (outcome, sequence);

CREATE INDEX ix_call_records_time_sequence ON call_records (event_time, sequence);

CREATE INDEX ix_call_records_type_sequence ON call_records (type, sequence);

CREATE TABLE dashboard_metric_hourly (
	bucket_start VARCHAR(13) NOT NULL, 
	total BIGINT NOT NULL, 
	success BIGINT NOT NULL, 
	final_failed BIGINT NOT NULL, 
	switch_requests BIGINT NOT NULL, 
	switch_count BIGINT NOT NULL, 
	switch_recovered BIGINT NOT NULL, 
	success_duration_total_ms FLOAT NOT NULL, 
	success_duration_count BIGINT NOT NULL, 
	PRIMARY KEY (bucket_start)
);

CREATE TABLE dashboard_metric_state (
	id SERIAL NOT NULL, 
	call_record_generation VARCHAR(64) NOT NULL, 
	last_sequence BIGINT NOT NULL, 
	status VARCHAR(16) NOT NULL, 
	failure_reason VARCHAR(255), 
	last_event_id VARCHAR(64), 
	last_event_at VARCHAR(40), 
	checkpoint_at VARCHAR(40), 
	retention_cutoff VARCHAR(13) NOT NULL, 
	revision INTEGER NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE editable_file_task (
	owner_id VARCHAR(256) NOT NULL, 
	task_id VARCHAR(160) NOT NULL, 
	storage_id VARCHAR(48) NOT NULL, 
	status VARCHAR(24) NOT NULL, 
	kind VARCHAR(8) NOT NULL, 
	model VARCHAR(128), 
	created_at VARCHAR(64) NOT NULL, 
	updated_at VARCHAR(64) NOT NULL, 
	created_ts FLOAT NOT NULL, 
	updated_ts FLOAT NOT NULL, 
	started_ts FLOAT, 
	ended_ts FLOAT, 
	result JSONB, 
	error TEXT, 
	account_email VARCHAR(320), 
	delete_pending BOOLEAN NOT NULL, 
	PRIMARY KEY (owner_id, task_id), 
	UNIQUE (storage_id)
);

CREATE INDEX ix_editable_file_task_delete_pending ON editable_file_task (delete_pending);

CREATE INDEX ix_editable_file_task_status ON editable_file_task (status);

CREATE INDEX ix_editable_file_task_updated_at ON editable_file_task (updated_at);

CREATE TABLE prompt_library_state (
	id SERIAL NOT NULL, 
	settings JSONB NOT NULL, 
	snapshot JSONB NOT NULL, 
	settings_revision INTEGER NOT NULL, 
	snapshot_revision INTEGER NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE proxy_configuration (
	id SERIAL NOT NULL, 
	data JSONB NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE remote_import_configurations (
	provider VARCHAR(32) NOT NULL, 
	items JSONB NOT NULL, 
	revision INTEGER NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (provider)
);

CREATE TABLE retention_cleanup_state (
	id SERIAL NOT NULL, 
	data JSONB NOT NULL, 
	revision INTEGER NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE storage_revisions (
	collection VARCHAR(32) NOT NULL, 
	version INTEGER NOT NULL, 
	PRIMARY KEY (collection)
);

CREATE TABLE system_settings (
	id SERIAL NOT NULL, 
	data JSONB NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	PRIMARY KEY (id)
);

CREATE TABLE dashboard_metric_model_hourly (
	bucket_start VARCHAR(13) NOT NULL, 
	model VARCHAR(255) NOT NULL, 
	success BIGINT NOT NULL, 
	success_duration_total_ms FLOAT NOT NULL, 
	success_duration_count BIGINT NOT NULL, 
	PRIMARY KEY (bucket_start, model), 
	FOREIGN KEY(bucket_start) REFERENCES dashboard_metric_hourly (bucket_start) ON DELETE CASCADE
);

CREATE INDEX ix_dashboard_metric_model_hourly_model ON dashboard_metric_model_hourly (model, bucket_start);

INSERT INTO storage_revisions(collection, version) VALUES ('accounts',0),('auth_keys',0);

-- +goose Down

-- +goose StatementBegin

DO $$ BEGIN RAISE EXCEPTION '不支持自动降级，请使用已验证的备份恢复流程'; END $$;

-- +goose StatementEnd
