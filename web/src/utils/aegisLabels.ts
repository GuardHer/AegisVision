/**
 * Unified human-readable enum & behavior label formatters for AegisVision UI.
 *
 * Keeps all backend enum codes (rule_mode, severity, handle_status, video_status,
 * node/task states, roles, behavior labels) cleanly mapped to localized display text.
 */

export type TranslateFn = (key: string) => string;

let globalLabelTranslator: TranslateFn | null = null;

/** Registers the app's vue-i18n translator for components outside vue-i18n context (e.g. CanvasOverlay). */
export function setLabelTranslator(fn: TranslateFn | null): void {
  globalLabelTranslator = fn;
}

const KNOWN_BEHAVIOR_LABELS = new Set<string>([
  "person",
  "no_helmet",
  "helmet",
  "no_vest",
  "vest",
  "no_gloves",
  "gloves",
  "no_goggles",
  "goggles",
  "no_mask",
  "mask",
  "smoking",
  "cigarette",
  "phone",
  "call",
  "fall",
  "fighting",
  "climb",
  "climbing",
  "sleep",
  "sleeping",
  "leave_post",
  "intrusion",
  "fire",
  "flame",
  "smoke",
  "forklift",
  "vehicle",
  "wear_esd_strap",
  "esd_strap",
  "scan_barcode",
  "barcode",
  "assemble",
  "inspect",
  "pack",
  "label_check"
]);

function translateOrFallback(
  key: string,
  fallback: string,
  t?: TranslateFn
): string {
  const fn = t ?? globalLabelTranslator;
  if (!fn) return fallback;
  try {
    const res = fn(key);
    if (res && res !== key) return res;
  } catch {
    // Fall through to fallback
  }
  return fallback;
}

function formatEnum(
  group: string,
  value: string | null | undefined,
  t?: TranslateFn,
  emptyFallback = "—"
): string {
  const raw = String(value ?? "").trim();
  if (!raw) return emptyFallback;
  return translateOrFallback(`aegis.enums.${group}.${raw}`, raw, t);
}

/** 规则模式: anomaly_detect / count_threshold / absence_detect / sop_sequence */
export function formatRuleMode(mode: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("ruleMode", mode, t);
}

/** 告警级别: critical / warning / info */
export function formatSeverity(severity: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("severity", severity, t);
}

/** 告警处置状态: unhandled / confirmed / false_positive / ignored */
export function formatHandleStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("handleStatus", status, t);
}

/** 视频证据状态: none / pending / recording / ready / failed */
export function formatVideoStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("videoStatus", status || "none", t);
}

/** 误报原因: lighting / occlusion / similar_action / model_drift / other */
export function formatFpReason(reason: string | null | undefined, t?: TranslateFn): string {
  const raw = String(reason ?? "").trim();
  if (!raw) return "—";
  const keyMap: Record<string, string> = {
    lighting: "aegis.alarms.fpLighting",
    occlusion: "aegis.alarms.fpOcclusion",
    similar_action: "aegis.alarms.fpSimilarAction",
    model_drift: "aegis.alarms.fpModelDrift",
    other: "aegis.alarms.fpOther"
  };
  const key = keyMap[raw];
  return key ? translateOrFallback(key, raw, t) : raw;
}

/** SOP 违规类型: missing_step / out_of_order / step_timeout */
export function formatViolationType(vt: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("violationType", vt, t);
}

/** SOP 步骤执行状态: pending / active / completed / skipped / violated */
export function formatSopStepStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("sopStepStatus", status, t);
}

/** SOP 触发方式: first_step / zone_entry */
export function formatTriggerMode(mode: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("triggerMode", mode, t);
}

/** 摄像头在线状态: online / offline */
export function formatCameraStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("cameraStatus", status, t);
}

/** 空间区域层级: factory / workshop / line / workstation */
export function formatZoneType(zoneType: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("zoneType", zoneType, t);
}

/** ROI 形态: polygon / full_frame */
export function formatRoiType(roiType: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("roiType", roiType, t);
}

/** 任务调度模式: auto / pinned */
export function formatScheduleMode(mode: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("scheduleMode", mode, t);
}

/** 算法版本状态: draft / active / deprecated */
export function formatVersionStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("versionStatus", status, t);
}

/** 算力节点状态: online / offline / draining */
export function formatNodeStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("nodeStatus", status, t);
}

/** 推理任务状态: running / starting / pending / stopped / failed / error */
export function formatTaskState(state: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("taskState", state, t);
}

/** 联动投递状态: pending / retrying / success / failed */
export function formatDeliveryStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("deliveryStatus", status, t);
}

/** 联动通道类型: webhook / wecom / dingtalk / sound_light */
export function formatChannelType(channelType: string | null | undefined, t?: TranslateFn): string {
  const raw = String(channelType ?? "").trim();
  if (!raw) return "—";
  const keyMap: Record<string, string> = {
    webhook: "aegis.channels.typeWebhook",
    wecom: "aegis.channels.typeWecom",
    dingtalk: "aegis.channels.typeDingtalk",
    sound_light: "aegis.channels.typeSoundLight"
  };
  const key = keyMap[raw];
  return key ? translateOrFallback(key, raw, t) : raw;
}

/** 用户角色: admin / operator / viewer */
export function formatUserRole(role: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("userRole", role, t);
}

/** 组件健康状态: healthy / degraded / not_configured */
export function formatHealthStatus(status: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("healthStatus", status, t);
}

/** 基础设施组件名称: postgresql / redis / rabbitmq / storage / mediamtx */
export function formatHealthComponent(name: string | null | undefined, t?: TranslateFn): string {
  const raw = String(name ?? "").trim().toLowerCase();
  if (!raw) return "—";
  return translateOrFallback(`aegis.enums.healthComponent.${raw}`, name || raw, t);
}

/** 审计资源类型: user / zone / camera / rule / schedule / algorithm / node / alarm / alarm_channel / system_settings */
export function formatAuditResource(resourceType: string | null | undefined, t?: TranslateFn): string {
  return formatEnum("auditResource", resourceType, t);
}

/** 星期 (1=Mon .. 7=Sun) */
export function formatWeekday(day: number, t?: TranslateFn): string {
  return translateOrFallback(`aegis.enums.weekday.d${day}`, `D${day}`, t);
}

/**
 * Resolves a behavior label code into a human-readable localized name.
 * Supports built-in EHS/SOP labels (`no_helmet` -> `未佩戴安全帽`) as well as
 * optional custom `code:Display Name` annotations.
 */
export function formatBehaviorLabel(label: string | null | undefined, t?: TranslateFn): string {
  const raw = String(label ?? "").trim();
  if (!raw) return "—";

  // Allow custom "code:名称" format if configured by an algorithm engineer
  const sepIdx = raw.indexOf(":");
  if (sepIdx > 0 && sepIdx < raw.length - 1) {
    return raw.slice(sepIdx + 1).trim();
  }

  const normalized = raw.toLowerCase();
  if (KNOWN_BEHAVIOR_LABELS.has(normalized)) {
    return translateOrFallback(`aegis.labels.${normalized}`, raw, t);
  }
  return translateOrFallback(`aegis.labels.${normalized}`, raw, t);
}

/**
 * Formats a behavior label with both readable name and raw code when they differ,
 * e.g. `未佩戴安全帽 (no_helmet)` for dropdowns and configuration panels.
 */
export function formatBehaviorLabelWithCode(
  label: string | null | undefined,
  t?: TranslateFn
): string {
  const raw = String(label ?? "").trim();
  if (!raw) return "—";
  const sepIdx = raw.indexOf(":");
  if (sepIdx > 0 && sepIdx < raw.length - 1) {
    const code = raw.slice(0, sepIdx).trim();
    const display = raw.slice(sepIdx + 1).trim();
    return `${display} (${code})`;
  }
  const display = formatBehaviorLabel(raw, t);
  return display !== raw ? `${display} (${raw})` : raw;
}
