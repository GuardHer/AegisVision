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

export interface AlgorithmVersionItem {
  id: string;
  algorithm_id: string;
  version: string;
  runtime_key: string;
  launch_spec: Record<string, any>;
  default_params: Record<string, any>;
  status: "draft" | "active" | "deprecated";
  created_at: string;
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
}

export interface ROIAlgorithmRuleItem {
  id: string;
  camera_id: string;
  roi_id: string;
  algorithm_version_id: string;
  name: string;
  rule_mode: "anomaly_detect" | "count_threshold" | "absence_detect";
  params: Record<string, any>;
  algo_params_override: Record<string, any>;
  schedule_mode: "auto" | "pinned";
  pinned_node_id?: string | null;
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
  handle_status: "unhandled" | "confirmed" | "false_positive";
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

export interface AlarmChannelItem {
  id: string;
  name: string;
  channel_type: string;
  config: Record<string, any>;
  enabled: boolean;
}

export interface UserItem {
  id: string;
  username: string;
  display_name: string;
  role: "admin" | "operator" | "viewer";
  enabled: boolean;
  last_login_at?: string | null;
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

// Cameras, Stream Token, ROIs & Rules
export const listCameras = () =>
  http.request<ApiEnvelope<CameraItem[]>>("get", "/api/v1/cameras");
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
  data: { handle_status: string; handle_note: string }
) => http.request<ApiEnvelope<AlarmItem>>("post", `/api/v1/alarms/${alarmId}/handle`, { data });
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

// Users
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
