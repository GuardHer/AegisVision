import { describe, expect, it } from "vitest";
import {
  formatBehaviorLabel,
  formatBehaviorLabelWithCode,
  formatHandleStatus,
  formatRuleMode,
  formatSeverity,
  formatVideoStatus,
  formatZoneType
} from "../aegisLabels";

const mockZhMap: Record<string, string> = {
  "aegis.enums.ruleMode.anomaly_detect": "异常行为检测",
  "aegis.enums.ruleMode.sop_sequence": "SOP 流程检测",
  "aegis.enums.handleStatus.unhandled": "待处理",
  "aegis.enums.severity.critical": "严重",
  "aegis.enums.videoStatus.recording": "录制中",
  "aegis.enums.zoneType.workshop": "车间",
  "aegis.labels.no_helmet": "未佩戴安全帽",
  "aegis.labels.smoking": "违规抽烟",
  "aegis.labels.wear_esd_strap": "佩戴防静电手环"
};

const tZh = (key: string) => mockZhMap[key] ?? key;

describe("aegisLabels formatters", () => {
  it("translates backend enum codes into readable UI text", () => {
    expect(formatRuleMode("anomaly_detect", tZh)).toBe("异常行为检测");
    expect(formatRuleMode("sop_sequence", tZh)).toBe("SOP 流程检测");
    expect(formatHandleStatus("unhandled", tZh)).toBe("待处理");
    expect(formatSeverity("critical", tZh)).toBe("严重");
    expect(formatVideoStatus("recording", tZh)).toBe("录制中");
    expect(formatZoneType("workshop", tZh)).toBe("车间");
  });

  it("formats built-in and custom behavior labels with readable names and codes", () => {
    expect(formatBehaviorLabel("no_helmet", tZh)).toBe("未佩戴安全帽");
    expect(formatBehaviorLabelWithCode("no_helmet", tZh)).toBe("未佩戴安全帽 (no_helmet)");
    expect(formatBehaviorLabelWithCode("wear_esd_strap", tZh)).toBe(
      "佩戴防静电手环 (wear_esd_strap)"
    );
    expect(formatBehaviorLabel("weld_spark:焊接火花", tZh)).toBe("焊接火花");
    expect(formatBehaviorLabelWithCode("weld_spark:焊接火花", tZh)).toBe("焊接火花 (weld_spark)");
    expect(formatBehaviorLabelWithCode("unknown_custom", tZh)).toBe("unknown_custom");
  });
});
