-- +goose Up
-- +goose StatementBegin

-- 1. 空间组织树（厂区 -> 车间 -> 产线 -> 工位）
CREATE TABLE zones (
    id UUID PRIMARY KEY,
    parent_id UUID NULL REFERENCES zones(id) ON DELETE RESTRICT,
    name VARCHAR(128) NOT NULL,
    code VARCHAR(64) NOT NULL DEFAULT '',
    zone_type VARCHAR(32) NOT NULL CHECK (zone_type IN ('factory', 'workshop', 'line', 'workstation')),
    sort_order INT NOT NULL DEFAULT 0,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_zones_parent_id ON zones (parent_id, sort_order);

-- 2. 用户车间级数据权限
ALTER TABLE users
    ADD COLUMN all_zones_access BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE user_zones (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    zone_id UUID NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, zone_id)
);

-- 3. 布防排班模板（支持白班/跨午夜夜班/节假日例外）
CREATE TABLE arming_schedules (
    id UUID PRIMARY KEY,
    name VARCHAR(128) NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai',
    weekly_windows JSONB NOT NULL DEFAULT '[]'::jsonb,
    exclude_dates JSONB NOT NULL DEFAULT '[]'::jsonb,
    include_dates JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 4. 摄像头与 ROI 空间/配色扩展
ALTER TABLE cameras
    ADD COLUMN zone_id UUID NULL REFERENCES zones(id) ON DELETE SET NULL,
    ADD COLUMN workstation_code VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN last_online_at TIMESTAMPTZ NULL;

CREATE INDEX idx_cameras_zone_id ON cameras (zone_id);

ALTER TABLE camera_rois
    ADD COLUMN color VARCHAR(16) NOT NULL DEFAULT '#38bdf8';

-- 5. 规则扩展：新增 sop_sequence 模式与布防排班引用
ALTER TABLE roi_algorithm_rules
    DROP CONSTRAINT IF EXISTS roi_algorithm_rules_rule_mode_check;

ALTER TABLE roi_algorithm_rules
    ADD CONSTRAINT roi_algorithm_rules_rule_mode_check
        CHECK (rule_mode IN ('anomaly_detect', 'count_threshold', 'absence_detect', 'sop_sequence')),
    ADD COLUMN schedule_id UUID NULL REFERENCES arming_schedules(id) ON DELETE SET NULL;

-- 6. 告警表扩展：冗余 zone_id、误报原因与 SOP 轨迹
ALTER TABLE alarms
    ADD COLUMN zone_id UUID NULL REFERENCES zones(id) ON DELETE SET NULL,
    ADD COLUMN false_positive_reason VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN sop_trace JSONB NULL;

CREATE INDEX idx_alarms_zone_time ON alarms (zone_id, triggered_at DESC);
CREATE INDEX idx_alarms_algo_handle ON alarms (algorithm_version_id, handle_status, triggered_at DESC);

-- 7. SOP 周期执行记录（支撑精确 SOP 合规率与平均周期耗时统计）
CREATE TABLE sop_cycles (
    id UUID PRIMARY KEY,
    rule_id UUID NOT NULL REFERENCES roi_algorithm_rules(id) ON DELETE CASCADE,
    camera_id UUID NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    zone_id UUID NULL REFERENCES zones(id) ON DELETE SET NULL,
    algorithm_version_id UUID NULL REFERENCES algorithm_versions(id) ON DELETE SET NULL,
    outcome VARCHAR(16) NOT NULL CHECK (outcome IN ('compliant', 'violated')),
    violation_type VARCHAR(32) NOT NULL DEFAULT '',
    alarm_id UUID NULL REFERENCES alarms(id) ON DELETE SET NULL,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ NOT NULL,
    duration_ms INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sop_cycles_zone_time ON sop_cycles (zone_id, ended_at DESC);
CREATE INDEX idx_sop_cycles_rule_time ON sop_cycles (rule_id, ended_at DESC);

-- 8. 监控分屏与定时轮巡预设方案
CREATE TABLE monitor_presets (
    id UUID PRIMARY KEY,
    owner_user_id UUID NULL REFERENCES users(id) ON DELETE CASCADE,
    name VARCHAR(128) NOT NULL,
    is_shared BOOLEAN NOT NULL DEFAULT FALSE,
    grid_size INT NOT NULL DEFAULT 4 CHECK (grid_size IN (1, 4, 9, 16)),
    camera_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    tour_interval_seconds INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_monitor_presets_owner ON monitor_presets (owner_user_id, is_shared);

-- 9. 通知通道显式路由规则
ALTER TABLE alarm_channels
    ADD COLUMN routing_rules JSONB NOT NULL DEFAULT '{"auto_route":false}'::jsonb;

-- 10. 操作审计日志
CREATE TABLE audit_logs (
    id UUID PRIMARY KEY,
    actor_user_id UUID NULL REFERENCES users(id) ON DELETE SET NULL,
    actor_username VARCHAR(64) NOT NULL DEFAULT '',
    action VARCHAR(64) NOT NULL,
    resource_type VARCHAR(64) NOT NULL,
    resource_id VARCHAR(128) NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    client_ip VARCHAR(64) NOT NULL DEFAULT '',
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_logs_created_at ON audit_logs (created_at DESC);
CREATE INDEX idx_audit_logs_resource ON audit_logs (resource_type, created_at DESC);
CREATE INDEX idx_audit_logs_actor ON audit_logs (actor_username, created_at DESC);

-- 11. 全局系统设置 KV
CREATE TABLE system_settings (
    key VARCHAR(64) PRIMARY KEY,
    value JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_by UUID NULL REFERENCES users(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS system_settings;
DROP TABLE IF EXISTS audit_logs;
ALTER TABLE alarm_channels DROP COLUMN IF EXISTS routing_rules;
DROP TABLE IF EXISTS monitor_presets;
DROP TABLE IF EXISTS sop_cycles;
DROP INDEX IF EXISTS idx_alarms_algo_handle;
DROP INDEX IF EXISTS idx_alarms_zone_time;
ALTER TABLE alarms
    DROP COLUMN IF EXISTS sop_trace,
    DROP COLUMN IF EXISTS false_positive_reason,
    DROP COLUMN IF EXISTS zone_id;
ALTER TABLE roi_algorithm_rules
    DROP COLUMN IF EXISTS schedule_id,
    DROP CONSTRAINT IF EXISTS roi_algorithm_rules_rule_mode_check;
ALTER TABLE roi_algorithm_rules
    ADD CONSTRAINT roi_algorithm_rules_rule_mode_check
        CHECK (rule_mode IN ('anomaly_detect', 'count_threshold', 'absence_detect'));
ALTER TABLE camera_rois DROP COLUMN IF EXISTS color;
DROP INDEX IF EXISTS idx_cameras_zone_id;
ALTER TABLE cameras
    DROP COLUMN IF EXISTS last_online_at,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS workstation_code,
    DROP COLUMN IF EXISTS zone_id;
DROP TABLE IF EXISTS arming_schedules;
DROP TABLE IF EXISTS user_zones;
ALTER TABLE users DROP COLUMN IF EXISTS all_zones_access;
DROP TABLE IF EXISTS zones;
-- +goose StatementEnd
