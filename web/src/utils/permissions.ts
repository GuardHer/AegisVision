export type AegisRole = "admin" | "operator" | "viewer";

export type AegisMenuKey =
  | "monitor"
  | "alarms"
  | "cameras"
  | "algorithms"
  | "nodes"
  | "channels"
  | "users";

const ALL_MENUS: AegisMenuKey[] = [
  "monitor",
  "alarms",
  "cameras",
  "algorithms",
  "nodes",
  "channels",
  "users"
];

export function getVisibleMenuKeys(role: string): AegisMenuKey[] {
  if (role === "admin") {
    return [...ALL_MENUS];
  }
  if (role === "operator" || role === "viewer") {
    return ["monitor", "alarms"];
  }
  return [];
}

export function canHandleAlarms(role: string): boolean {
  return role === "admin" || role === "operator";
}

export function canManageConfig(role: string): boolean {
  return role === "admin";
}

export function rolePermissions(role: string): string[] {
  if (role === "admin") {
    return ["*:*:*"];
  }
  if (role === "operator") {
    return ["monitor:view", "alarms:view", "alarms:handle", "deliveries:retry"];
  }
  if (role === "viewer") {
    return ["monitor:view", "alarms:view"];
  }
  return [];
}
