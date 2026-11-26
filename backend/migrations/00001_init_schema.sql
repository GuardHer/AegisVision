-- +goose Up
-- +goose StatementBegin

-- 1. 用户与认证域
CREATE TABLE users (
    id UUID PRIMARY KEY,
    username VARCHAR(64) NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    role VARCHAR(32) NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE refresh_tokens (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash CHAR(64) NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_refresh_tokens_user_id ON refresh_tokens (user_id);
CREATE INDEX idx_refresh_tokens_expires_at ON refresh_tokens (expires_at);

-- 2. 算法与版本管理域
CREATE TABLE algorithms (
    id UUID PRIMARY KEY,
    algorithm_key VARCHAR(64) NOT NULL UNIQUE,
    name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    supported_labels JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE algorithm_versions (
    id UUID PRIMARY KEY,
    algorithm_id UUID NOT NULL REFERENCES algorithms(id) ON DELETE RESTRICT,
    version VARCHAR(64) NOT NULL,
    runtime_key VARCHAR(128) NOT NULL,
    launch_spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    default_params JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(32) NOT NULL CHECK (status IN ('draft', 'active', 'deprecated')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (algorithm_id, version)
);

CREATE INDEX idx_algo_versions_runtime_key ON algorithm_versions (runtime_key);

-- 3. 摄像头、ROI 与区域规则域
CREATE TABLE cameras (
    id UUID PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    location VARCHAR(256) NOT NULL DEFAULT '',
    rtsp_url TEXT NOT NULL,
    sub_stream_url TEXT NOT NULL DEFAULT '',
    stream_path VARCHAR(128) NOT NULL UNIQUE,
    codec_hint VARCHAR(16) NOT NULL DEFAULT 'h264',
    status VARCHAR(32) NOT NULL DEFAULT 'offline',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE camera_rois (
    id UUID PRIMARY KEY,
    camera_id UUID NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    roi_type VARCHAR(32) NOT NULL CHECK (roi_type IN ('polygon', 'full_frame')),
    points JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_camera_rois_camera_id ON camera_rois (camera_id);

CREATE TABLE roi_algorithm_rules (
    id UUID PRIMARY KEY,
    camera_id UUID NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    roi_id UUID NOT NULL REFERENCES camera_rois(id) ON DELETE CASCADE,
    algorithm_version_id UUID NOT NULL REFERENCES algorithm_versions(id) ON DELETE RESTRICT,
    name VARCHAR(128) NOT NULL,
    rule_mode VARCHAR(32) NOT NULL CHECK (rule_mode IN ('anomaly_detect', 'count_threshold', 'absence_detect')),
    params JSONB NOT NULL,
    algo_params_override JSONB NOT NULL DEFAULT '{}'::jsonb,
    schedule_mode VARCHAR(32) NOT NULL DEFAULT 'auto' CHECK (schedule_mode IN ('auto', 'pinned')),
    pinned_node_id VARCHAR(128) NULL,
    record_video BOOLEAN NOT NULL DEFAULT FALSE,
    video_pre_seconds INT NOT NULL DEFAULT 5,
    video_post_seconds INT NOT NULL DEFAULT 5,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_rules_camera_enabled ON roi_algorithm_rules (camera_id, enabled);
CREATE INDEX idx_rules_algo_version ON roi_algorithm_rules (algorithm_version_id);

-- 4. 节点集群与租约任务域
CREATE TABLE agent_nodes (
    node_id VARCHAR(128) PRIMARY KEY,
    hostname VARCHAR(128) NOT NULL,
    ip_addr VARCHAR(64) NOT NULL,
    runtime_keys JSONB NOT NULL DEFAULT '[]'::jsonb,
    max_tasks INT NOT NULL DEFAULT 8,
    running_tasks INT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT 'offline',
    meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_heartbeat_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE inference_tasks (
    id UUID PRIMARY KEY,
    camera_id UUID NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    algorithm_version_id UUID NOT NULL REFERENCES algorithm_versions(id) ON DELETE RESTRICT,
    runtime_key VARCHAR(128) NOT NULL,
    assigned_node_id VARCHAR(128) NULL REFERENCES agent_nodes(node_id) ON DELETE SET NULL,
    ingest_instance_id VARCHAR(128) NOT NULL DEFAULT '',
    desired_state VARCHAR(32) NOT NULL CHECK (desired_state IN ('running', 'stopped')),
    actual_state VARCHAR(32) NOT NULL DEFAULT 'pending',
    lease_id UUID NULL,
    lease_expires_at TIMESTAMPTZ NULL,
    config_version BIGINT NOT NULL DEFAULT 1,
    last_frame_at TIMESTAMPTZ NULL,
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX uq_active_task_cam_ver
    ON inference_tasks (camera_id, algorithm_version_id)
    WHERE desired_state = 'running';

-- 5. 报警记录与可扩展投递域
CREATE TABLE alarm_channels (
    id UUID PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    channel_type VARCHAR(32) NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE rule_alarm_channels (
    rule_id UUID NOT NULL REFERENCES roi_algorithm_rules(id) ON DELETE CASCADE,
    channel_id UUID NOT NULL REFERENCES alarm_channels(id) ON DELETE CASCADE,
    PRIMARY KEY (rule_id, channel_id)
);

CREATE TABLE alarms (
    id UUID PRIMARY KEY,
    event_dedup_key VARCHAR(128) NULL UNIQUE,
    camera_id UUID NOT NULL REFERENCES cameras(id),
    roi_id UUID NULL REFERENCES camera_rois(id) ON DELETE SET NULL,
    rule_id UUID NULL REFERENCES roi_algorithm_rules(id) ON DELETE SET NULL,
    algorithm_version_id UUID NULL REFERENCES algorithm_versions(id) ON DELETE SET NULL,
    rule_mode VARCHAR(32) NOT NULL,
    title VARCHAR(256) NOT NULL,
    severity VARCHAR(16) NOT NULL DEFAULT 'warning',
    triggered_at TIMESTAMPTZ NOT NULL,
    detected_count INT NOT NULL DEFAULT 0,
    detections_snapshot JSONB NOT NULL DEFAULT '[]'::jsonb,
    roi_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    image_object_key TEXT NOT NULL DEFAULT '',
    video_object_key TEXT NOT NULL DEFAULT '',
    video_status VARCHAR(16) NOT NULL DEFAULT 'none',
    handle_status VARCHAR(32) NOT NULL DEFAULT 'unhandled',
    handled_by UUID NULL REFERENCES users(id) ON DELETE SET NULL,
    handled_at TIMESTAMPTZ NULL,
    handle_note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_alarms_triggered_at ON alarms (triggered_at DESC);
CREATE INDEX idx_alarms_camera_time ON alarms (camera_id, triggered_at DESC);
CREATE INDEX idx_alarms_handle_status ON alarms (handle_status, triggered_at DESC);

CREATE TABLE alarm_deliveries (
    id UUID PRIMARY KEY,
    alarm_id UUID NOT NULL REFERENCES alarms(id) ON DELETE CASCADE,
    channel_id UUID NOT NULL REFERENCES alarm_channels(id) ON DELETE CASCADE,
    channel_type VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    max_attempts INT NOT NULL DEFAULT 4,
    last_status_code INT NOT NULL DEFAULT 0,
    last_latency_ms INT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_deliveries_alarm_id ON alarm_deliveries (alarm_id);
CREATE INDEX idx_deliveries_status ON alarm_deliveries (status, updated_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS alarm_deliveries;
DROP TABLE IF EXISTS alarms;
DROP TABLE IF EXISTS rule_alarm_channels;
DROP TABLE IF EXISTS alarm_channels;
DROP TABLE IF EXISTS inference_tasks;
DROP TABLE IF EXISTS agent_nodes;
DROP TABLE IF EXISTS roi_algorithm_rules;
DROP TABLE IF EXISTS camera_rois;
DROP TABLE IF EXISTS cameras;
DROP TABLE IF EXISTS algorithm_versions;
DROP TABLE IF EXISTS algorithms;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;
-- +goose StatementEnd
