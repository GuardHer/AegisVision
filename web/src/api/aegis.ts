import { http } from "@/utils/http";
import { getToken } from "@/utils/auth";
import type { NormalizedPoint } from "@/utils/videoRect";

export interface ApiEnvelope<T> {
  code: number;
  message: string;
  data: T;
}

export interface AlgorithmItem {
  id: string;
  algorithm_key: string;
  name: string;
  description: string;
  supported_labels: string[];
  created_at: string;
}

export interface AlgorithmVersionStats30d {
  total_alarms: number;
  confirmed_count: number;
  false_positive_count: number;
  false_positive_rate: number;
}

export interface AlgorithmVersionItem {
  id: string;
  algorithm_id: string;
  version: string;
  runtime_key: string;
  launch_spec: Record<string, any>;
  default_params: Record<string, any>;
  status: "draft" | "active" | "deprecated";
  created_at: string;
  stats_30d?: AlgorithmVersionStats30d;
}

export interface ZoneTreeNode {
  id: string;
  parent_id?: string | null;
  name: string;
  code: string;
  zone_type: "factory" | "workshop" | "line" | "workstation";
  sort_order: number;
  description: string;
  camera_count: number;
  online_camera_count: number;
  children: ZoneTreeNode[];
}

export interface MonitorPresetItem {
  id: string;
  owner_user_id?: string | null;
  name: string;
  is_shared: boolean;
  grid_size: 1 | 4 | 9 | 16;
  camera_ids: string[];
  tour_interval_seconds: number;
  created_at: string;
}

export interface DashboardKPIs {
  today_alarms: number;
  yesterday_alarms: number;
  unhandled_alarms: number;
  confirmed_rate: number;
  false_positive_rate: number;
  today_sop_violations: number;
  sop_compliance_rate: number;
  cameras_online: number;
  cameras_total: number;
  nodes_online: number;
  nodes_total: number;
  running_tasks: number;
}

export interface DashboardOverview {
  kpis: DashboardKPIs;
  trend_7d: Array<{ date: string; safety_count: number; sop_count: number }>;
  top_zones: Array<{ zone_id?: string | null; zone_name: string; alarm_count: number }>;
  by_rule_mode: Array<{ rule_mode: string; count: number }>;
  recent_alerts: Array<{
    id: string;
    camera_id: string;
    camera_name: string;
    zone_id?: string | null;
    zone_name: string;
    rule_mode: string;
    title: string;
    severity: "info" | "warning" | "critical";
    handle_status: string;
    image_object_key: string;
    triggered_at: string;
  }>;
}

export interface CameraItem {
  id: string;
  name: string;
  location: string;
  rtsp_url: string;
  sub_stream_url: string;
  stream_path: string;
  codec_hint: string;
  status: string;
  enabled: boolean;
  zone_id?: string | null;
  workstation_code?: string;
  tags?: string[];
  last_online_at?: string | null;
  created_at: string;
}

export interface StreamTokenResponse {
  token: string;
  stream_path: string;
  whep_url: string;
  expires_in: number;
}

export interface CameraROIItem {
  id: string;
  camera_id: string;
  name: string;
  roi_type: "polygon" | "full_frame";
  points: NormalizedPoint[];
  enabled: boolean;
  color?: string;
}

export interface WeeklyWindow {
  days: number[];
  start: string;
  end: string;
}

export interface IncludeDateWindow {
  date: string;
  start?: string;
  end?: string;
}

export interface ArmingScheduleItem {
  id: string;
  name: string;
  description: string;
  timezone: string;
  weekly_windows: WeeklyWindow[];
  exclude_dates: string[];
  include_dates: IncludeDateWindow[];
  enabled: boolean;
  rule_count?: number;
  created_at: string;
}

export interface SOPStepSpec {
  step_id: string;
  name: string;
  labels: string[];
  roi_id?: string;
  min_confidence: number;
  hit_point?: string;
  min_dwell_frames?: number;
  max_wait_seconds?: number;
  required: boolean;
}

export interface SOPStepTraceItem {
  step_id: string;
  name: string;
  status: "pending" | "active" | "completed" | "skipped" | "violated";
  completed_at?: string | null;
  elapsed_ms?: number;
}

export interface SOPTrace {
  trigger_mode: string;
  violation_type: string;
  violated_step_id?: string;
  violated_step_name?: string;
  started_at: string;
  ended_at: string;
  duration_ms: number;
  steps: SOPStepTraceItem[];
}

export interface SOPRuleProgress {
  rule_id: string;
  rule_name: string;
  state: "idle" | "in_progress" | "rearm";
  current_step_index: number;
  steps: SOPStepTraceItem[];
}

export interface AnalyticsSummary {
  kpis: {
    total_alarms: number;
    safety_alarms: number;
    sop_alarms: number;
    confirmed_count: number;
    false_positive_count: number;
    false_positive_rate: number;
    sop_compliant_cycles: number;
    sop_violated_cycles: number;
    sop_compliance_rate: number;
    avg_sop_duration_ms: number;
  };
  trend: Array<{ bucket: string; safety_count: number; sop_count: number }>;
  by_zone: Array<{
    zone_id?: string | null;
    zone_name: string;
    safety_count: number;
    sop_count: number;
    confirmed_count: number;
    false_positive_count: number;
  }>;
  top_sop_steps: Array<{ step_name: string; violation_type: string; count: number }>;
  algo_precision: Array<{
    algorithm_version_id: string;
    algorithm_name: string;
    version: string;
    total_alarms: number;
    confirmed_count: number;
    false_positive_count: number;
    false_positive_rate: number;
  }>;
}

export interface ROIAlgorithmRuleItem {
  id: string;
  camera_id: string;
  roi_id: string;
  algorithm_version_id: string;
  name: string;
  rule_mode: "anomaly_detect" | "count_threshold" | "absence_detect" | "sop_sequence";
  params: Record<string, any>;
  algo_params_override: Record<string, any>;
  schedule_mode: "auto" | "pinned";
  pinned_node_id?: string | null;
  schedule_id?: string | null;
  record_video: boolean;
  video_pre_seconds: number;
  video_post_seconds: number;
  enabled: boolean;
}

export interface AgentNodeItem {
  node_id: string;
  hostname: string;
  ip_addr: string;
  runtime_keys: string[];
  max_tasks: number;
  running_tasks: number;
  status: "online" | "offline" | "draining";
  meta: Record<string, any>;
  last_heartbeat_at?: string | null;
}

export interface InferenceTaskItem {
  id: string;
  camera_id: string;
  algorithm_version_id: string;
  runtime_key: string;
  assigned_node_id?: string | null;
  desired_state: string;
  actual_state: string;
  lease_expires_at?: string | null;
  last_frame_at?: string | null;
  error_message: string;
}

export interface DetectionBox {
  bbox: [number, number, number, number];
  label: string;
  confidence: number;
  track_id?: string;
  is_abnormal?: boolean;
}

export interface AlarmItem {
  id: string;
  camera_id: string;
  zone_id?: string | null;
  roi_id?: string | null;
  rule_id?: string | null;
  algorithm_version_id?: string | null;
  rule_mode: string;
  title: string;
  severity: "info" | "warning" | "critical";
  triggered_at: string;
  detected_count: number;
  detections_snapshot: DetectionBox[];
  roi_snapshot: { id?: string; name?: string; roi_type?: string; points?: NormalizedPoint[] };
  image_object_key: string;
  video_object_key: string;
  video_status: "none" | "recording" | "ready" | "failed";
  handle_status: "unhandled" | "confirmed" | "false_positive" | "ignored";
  false_positive_reason?: string;
  sop_trace?: SOPTrace | null;
  handled_by?: string | null;
  handled_at?: string | null;
  handle_note: string;
}

export interface AlarmDeliveryItem {
  id: string;
  alarm_id: string;
  channel_id: string;
  channel_type: string;
  status: string;
  attempts: number;
  max_attempts: number;
  last_status_code: number;
  last_latency_ms: number;
  last_error: string;
  updated_at: string;
}

export interface ChannelRoutingRules {
  auto_route?: boolean;
  severities?: string[];
  zone_ids?: string[];
  rule_modes?: string[];
}

export interface AlarmChannelItem {
  id: string;
  name: string;
  channel_type: "webhook" | "wecom" | "dingtalk" | "sound_light" | string;
  config: Record<string, any>;
  routing_rules?: ChannelRoutingRules;
  enabled: boolean;
}

export interface UserItem {
  id: string;
  username: string;
  display_name: string;
  role: "admin" | "operator" | "viewer";
  enabled: boolean;
  all_zones_access?: boolean;
  last_login_at?: string | null;
}

export interface UserZonesScope {
  user_id: string;
  all_zones_access: boolean;
  zone_ids: string[];
}

export interface FalsePositiveSampleItem {
  alarm_id: string;
  camera_id: string;
  camera_name: string;
  zone_id?: string | null;
  zone_name: string;
  algorithm_version_id?: string | null;
  algorithm_name: string;
  algorithm_version: string;
  rule_mode: string;
  title: string;
  severity: "info" | "warning" | "critical";
  triggered_at: string;
  handled_at?: string | null;
  false_positive_reason: string;
  handle_note: string;
  detections_snapshot: DetectionBox[];
  roi_snapshot: Record<string, any>;
  image_url: string;
}

export interface AuditLogItem {
  id: string;
  actor_user_id?: string | null;
  actor_username: string;
  action: string;
  resource_type: string;
  resource_id: string;
  summary: string;
  detail: Record<string, any>;
  client_ip: string;
  request_id: string;
  created_at: string;
}

export interface PlatformSettings {
  platform_title: string;
  default_alarm_chime: boolean;
  popup_min_severity: "info" | "warning" | "critical";
  max_export_rows: number;
  default_timezone: string;
}

export interface SystemComponentHealth {
  name: string;
  status: "healthy" | "degraded" | "not_configured" | string;
  detail?: string;
  latency_ms: number;
}

export interface SystemHealthPayload {
  checked_at: string;
  components: SystemComponentHealth[];
  cluster: {
    nodes_online: number;
    nodes_total: number;
    running_tasks: number;
    cameras_online: number;
    cameras_total: number;
  };
}

// Algorithms & Versions
export const listAlgorithms = () =>
  http.request<ApiEnvelope<AlgorithmItem[]>>("get", "/api/v1/algorithms");
export const createAlgorithm = (data: Record<string, any>) =>
  http.request<ApiEnvelope<AlgorithmItem>>("post", "/api/v1/algorithms", { data });
export const updateAlgorithm = (id: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<AlgorithmItem>>("put", `/api/v1/algorithms/${id}`, { data });
export const deleteAlgorithm = (id: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/algorithms/${id}`);
export const listAlgorithmVersions = (algorithmId: string) =>
  http.request<ApiEnvelope<AlgorithmVersionItem[]>>(
    "get",
    `/api/v1/algorithms/${algorithmId}/versions`
  );
export const createAlgorithmVersion = (
  algorithmId: string,
  data: Record<string, any>
) =>
  http.request<ApiEnvelope<AlgorithmVersionItem>>(
    "post",
    `/api/v1/algorithms/${algorithmId}/versions`,
    { data }
  );
export const updateAlgorithmVersion = (
  versionId: string,
  data: Record<string, any>
) =>
  http.request<ApiEnvelope<AlgorithmVersionItem>>(
    "put",
    `/api/v1/algorithm-versions/${versionId}`,
    { data }
  );

// Dashboard, Analytics, Arming Schedules, Spatial Zones & Monitor Tour Presets
export const getDashboardOverview = (tz = "Asia/Shanghai") =>
  http.request<ApiEnvelope<DashboardOverview>>("get", "/api/v1/dashboard/overview", {
    params: { tz }
  });

export const getAnalyticsSummary = (params: Record<string, any>) =>
  http.request<ApiEnvelope<AnalyticsSummary>>("get", "/api/v1/analytics/summary", { params });

export const exportAnalyticsCSVBlob = (params: Record<string, any>): Promise<Blob> =>
  http.request<Blob>("get", "/api/v1/analytics/export", {
    params,
    responseType: "blob",
    timeout: 60000
  });

export const listSchedules = () =>
  http.request<ApiEnvelope<ArmingScheduleItem[]>>("get", "/api/v1/schedules");
export const createSchedule = (data: Record<string, any>) =>
  http.request<ApiEnvelope<ArmingScheduleItem>>("post", "/api/v1/schedules", { data });
export const updateSchedule = (id: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<ArmingScheduleItem>>("put", `/api/v1/schedules/${id}`, { data });
export const deleteSchedule = (id: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/schedules/${id}`);

export const getZonesTree = () =>
  http.request<ApiEnvelope<ZoneTreeNode[]>>("get", "/api/v1/zones/tree");
export const createZone = (data: Record<string, any>) =>
  http.request<ApiEnvelope<ZoneTreeNode>>("post", "/api/v1/zones", { data });
export const updateZone = (id: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<ZoneTreeNode>>("put", `/api/v1/zones/${id}`, { data });
export const deleteZone = (id: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/zones/${id}`);

export const listMonitorPresets = () =>
  http.request<ApiEnvelope<MonitorPresetItem[]>>("get", "/api/v1/monitor/presets");
export const createMonitorPreset = (data: Record<string, any>) =>
  http.request<ApiEnvelope<MonitorPresetItem>>("post", "/api/v1/monitor/presets", { data });
export const updateMonitorPreset = (id: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<MonitorPresetItem>>("put", `/api/v1/monitor/presets/${id}`, { data });
export const deleteMonitorPreset = (id: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/monitor/presets/${id}`);

// Cameras, Stream Token, ROIs & Rules
export const listCameras = (zoneId?: string) =>
  http.request<ApiEnvelope<CameraItem[]>>("get", "/api/v1/cameras", {
    params: zoneId ? { zone_id: zoneId } : undefined
  });
export const batchUpdateCameras = (data: {
  camera_ids: string[];
  action: "enable" | "disable" | "move_zone";
  zone_id?: string;
}) => http.request<ApiEnvelope<{ updated_count: number }>>("post", "/api/v1/cameras/batch", { data });
export const importCamerasCSV = (csv: string) =>
  http.request<ApiEnvelope<{ created_count: number; cameras: CameraItem[]; errors: string[] }>>(
    "post",
    "/api/v1/cameras/import",
    { data: { csv } }
  );
export const getCamera = (id: string) =>
  http.request<ApiEnvelope<CameraItem>>("get", `/api/v1/cameras/${id}`);
export const createCamera = (data: Record<string, any>) =>
  http.request<ApiEnvelope<CameraItem>>("post", "/api/v1/cameras", { data });
export const updateCamera = (id: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<CameraItem>>("put", `/api/v1/cameras/${id}`, { data });
export const deleteCamera = (id: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/cameras/${id}`);
export const createStreamToken = (cameraId: string, stream: "main" | "sub" = "main") =>
  http.request<ApiEnvelope<StreamTokenResponse>>(
    "post",
    `/api/v1/cameras/${cameraId}/stream-token`,
    { params: { stream } }
  );

export const listROIs = (cameraId: string) =>
  http.request<ApiEnvelope<CameraROIItem[]>>("get", `/api/v1/cameras/${cameraId}/rois`);
export const createROI = (cameraId: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<CameraROIItem>>("post", `/api/v1/cameras/${cameraId}/rois`, { data });
export const updateROI = (roiId: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<CameraROIItem>>("put", `/api/v1/rois/${roiId}`, { data });
export const deleteROI = (roiId: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/rois/${roiId}`);

export const listRules = (cameraId: string) =>
  http.request<ApiEnvelope<ROIAlgorithmRuleItem[]>>(
    "get",
    `/api/v1/cameras/${cameraId}/rules`
  );
export const createRule = (cameraId: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<ROIAlgorithmRuleItem>>(
    "post",
    `/api/v1/cameras/${cameraId}/rules`,
    { data }
  );
export const updateRule = (ruleId: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<ROIAlgorithmRuleItem>>(
    "put",
    `/api/v1/rules/${ruleId}`,
    { data }
  );
export const deleteRule = (ruleId: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/rules/${ruleId}`);

// Nodes & Tasks
export const listNodes = () =>
  http.request<ApiEnvelope<AgentNodeItem[]>>("get", "/api/v1/nodes");
export const updateNodeStatus = (nodeId: string, status: string) =>
  http.request<ApiEnvelope<AgentNodeItem>>("put", `/api/v1/nodes/${nodeId}/status`, {
    data: { status }
  });
export const listTasks = () =>
  http.request<ApiEnvelope<InferenceTaskItem[]>>("get", "/api/v1/tasks");
export const rescheduleTask = (taskId: string) =>
  http.request<ApiEnvelope<any>>("post", `/api/v1/tasks/${taskId}/reschedule`);

// Alarms & Deliveries
export const listAlarms = (params: Record<string, any>) =>
  http.request<
    ApiEnvelope<{ items: AlarmItem[]; total: number; page: number; page_size: number }>
  >("get", "/api/v1/alarms", { params });
export const getAlarm = (alarmId: string) =>
  http.request<ApiEnvelope<AlarmItem>>("get", `/api/v1/alarms/${alarmId}`);
export const handleAlarm = (
  alarmId: string,
  data: { handle_status: string; false_positive_reason?: string; handle_note: string }
) => http.request<ApiEnvelope<AlarmItem>>("post", `/api/v1/alarms/${alarmId}/handle`, { data });

export const batchHandleAlarms = (data: {
  alarm_ids: string[];
  handle_status: string;
  false_positive_reason?: string;
  handle_note: string;
}) => http.request<ApiEnvelope<{ updated_count: number }>>("post", "/api/v1/alarms/batch-handle", { data });

export const exportAlarmsBlob = (payload: Record<string, any>): Promise<Blob> =>
  http.request<Blob>("post", "/api/v1/alarms/export", {
    data: payload,
    responseType: "blob",
    timeout: 120000
  });
export const listAlarmDeliveries = (alarmId: string) =>
  http.request<ApiEnvelope<AlarmDeliveryItem[]>>(
    "get",
    `/api/v1/alarms/${alarmId}/deliveries`
  );
export const retryDelivery = (deliveryId: string) =>
  http.request<ApiEnvelope<AlarmDeliveryItem>>(
    "post",
    `/api/v1/deliveries/${deliveryId}/retry`
  );

export function evidenceImageURL(alarmId: string): string {
  const token = getToken()?.accessToken ?? "";
  return `/api/v1/evidence/${alarmId}/image?token=${encodeURIComponent(token)}`;
}

export function evidenceVideoURL(alarmId: string): string {
  const token = getToken()?.accessToken ?? "";
  return `/api/v1/evidence/${alarmId}/video?token=${encodeURIComponent(token)}`;
}

export function buildRealtimeWSURL(): string {
  const token = getToken()?.accessToken ?? "";
  const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${window.location.host}/ws/realtime?token=${encodeURIComponent(token)}`;
}

// Channels
export const listChannels = () =>
  http.request<ApiEnvelope<AlarmChannelItem[]>>("get", "/api/v1/channels");
export const createChannel = (data: Record<string, any>) =>
  http.request<ApiEnvelope<AlarmChannelItem>>("post", "/api/v1/channels", { data });
export const updateChannel = (id: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<AlarmChannelItem>>("put", `/api/v1/channels/${id}`, { data });
export const deleteChannel = (id: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/channels/${id}`);
export const testChannel = (id: string) =>
  http.request<ApiEnvelope<{ status_code: number; latency_ms: number; ok: boolean }>>(
    "post",
    `/api/v1/channels/${id}/test`
  );
export const resetChannel = (id: string) =>
  http.request<ApiEnvelope<{ reset_enqueued: boolean; channel_id: string }>>(
    "post",
    `/api/v1/channels/${id}/reset`
  );

// False-Positive Training Datasets
export const listFalsePositiveDataset = (params: Record<string, any>) =>
  http.request<
    ApiEnvelope<{ items: FalsePositiveSampleItem[]; total: number; page: number; page_size: number }>
  >("get", "/api/v1/datasets/false-positives", { params });

export const exportFalsePositiveDatasetBlob = (payload: Record<string, any>): Promise<Blob> =>
  http.request<Blob>("post", "/api/v1/datasets/export", {
    data: payload,
    responseType: "blob",
    timeout: 120000
  });

// Users & Workshop Scope
export const listUsers = () =>
  http.request<ApiEnvelope<UserItem[]>>("get", "/api/v1/users");
export const createUser = (data: Record<string, any>) =>
  http.request<ApiEnvelope<UserItem>>("post", "/api/v1/users", { data });
export const updateUser = (id: string, data: Record<string, any>) =>
  http.request<ApiEnvelope<UserItem>>("put", `/api/v1/users/${id}`, { data });
export const deleteUser = (id: string) =>
  http.request<ApiEnvelope<any>>("delete", `/api/v1/users/${id}`);
export const resetUserPassword = (id: string, newPassword: string) =>
  http.request<ApiEnvelope<any>>("post", `/api/v1/users/${id}/reset-password`, {
    data: { new_password: newPassword }
  });
export const getUserZones = (id: string) =>
  http.request<ApiEnvelope<UserZonesScope>>("get", `/api/v1/users/${id}/zones`);
export const updateUserZones = (id: string, data: { all_zones_access: boolean; zone_ids: string[] }) =>
  http.request<ApiEnvelope<UserZonesScope>>("put", `/api/v1/users/${id}/zones`, { data });

// Governance: Audit Logs, Global Settings & System Health
export const listAuditLogs = (params: Record<string, any>) =>
  http.request<
    ApiEnvelope<{ items: AuditLogItem[]; total: number; page: number; page_size: number }>
  >("get", "/api/v1/audit-logs", { params });
export const getPlatformSettings = () =>
  http.request<ApiEnvelope<PlatformSettings>>("get", "/api/v1/settings");
export const updatePlatformSettings = (data: PlatformSettings) =>
  http.request<ApiEnvelope<PlatformSettings>>("put", "/api/v1/settings", { data });
export const getSystemHealth = () =>
  http.request<ApiEnvelope<SystemHealthPayload>>("get", "/api/v1/system/health");
