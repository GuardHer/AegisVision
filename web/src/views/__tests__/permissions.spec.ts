import { describe, it, expect } from "vitest";
import {
  canHandleAlarms,
  canManageConfig,
  getVisibleMenuKeys
} from "@/utils/permissions";

describe("RBAC frontend menu & action visibility", () => {
  it("viewer sees monitor and alarms only, cannot handle alarms or manage config", () => {
    const menus = getVisibleMenuKeys("viewer");
    expect(menus).toContain("monitor");
    expect(menus).toContain("alarms");
    expect(menus).not.toContain("cameras");
    expect(menus).not.toContain("algorithms");
    expect(menus).not.toContain("nodes");
    expect(menus).not.toContain("channels");
    expect(menus).not.toContain("users");

    expect(canHandleAlarms("viewer")).toBe(false);
    expect(canManageConfig("viewer")).toBe(false);
  });

  it("operator sees monitor and alarms and can handle alarms, but cannot manage config", () => {
    const menus = getVisibleMenuKeys("operator");
    expect(menus).toContain("monitor");
    expect(menus).toContain("alarms");
    expect(menus).not.toContain("cameras");
    expect(menus).not.toContain("users");

    expect(canHandleAlarms("operator")).toBe(true);
    expect(canManageConfig("operator")).toBe(false);
  });

  it("admin sees all menus and has full action permissions", () => {
    const menus = getVisibleMenuKeys("admin");
    expect(menus).toEqual(
      expect.arrayContaining([
        "monitor",
        "alarms",
        "cameras",
        "algorithms",
        "nodes",
        "channels",
        "users"
      ])
    );

    expect(canHandleAlarms("admin")).toBe(true);
    expect(canManageConfig("admin")).toBe(true);
  });
});
