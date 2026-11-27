import { $t } from "@/plugins/i18n";
const Layout = () => import("@/layout/index.vue");

/**
 * AegisVision 企业控制台 4 大业务域分组导航（首页「运营态势总览」见 home.ts rank: 0）：
 * 1. 监控与告警 (/ops)       — 实时值班中心、告警处置台、统计分析报表
 * 2. 点位与布防 (/assets)    — 厂区点位管理（含点位智能配置）、布防排班模板
 * 3. 算法与算力 (/ai)        — 算法仓库、误报样本集、算力集群与任务
 * 4. 系统治理 (/system)      — 联动通知通道、用户与车间权限、操作审计日志、系统配置与健康
 *
 * 所有子路由保留原有绝对路径，确保页面内跳转与书签 100% 兼容。
 */
export default [
  {
    path: "/ops",
    component: Layout,
    redirect: "/monitor/index",
    meta: {
      icon: "ep/monitor",
      title: $t("menus.aegisGroupOps"),
      rank: 1
    },
    children: [
      {
        path: "/monitor/index",
        name: "AegisMonitor",
        component: () => import("@/views/monitor/index.vue"),
        meta: {
          icon: "ep/video-camera",
          title: $t("menus.aegisMonitor"),
          roles: ["admin", "operator", "viewer"],
          showParent: true
        }
      },
      {
        path: "/alarms/index",
        name: "AegisAlarms",
        component: () => import("@/views/alarms/index.vue"),
        meta: {
          icon: "ep/bell",
          title: $t("menus.aegisAlarms"),
          roles: ["admin", "operator", "viewer"],
          showParent: true
        }
      },
      {
        path: "/analytics/index",
        name: "AegisAnalytics",
        component: () => import("@/views/analytics/index.vue"),
        meta: {
          icon: "ep/histogram",
          title: $t("menus.aegisAnalytics"),
          roles: ["admin", "operator", "viewer"],
          showParent: true
        }
      }
    ]
  },
  {
    path: "/assets",
    component: Layout,
    redirect: "/cameras/index",
    meta: {
      icon: "ep/camera",
      title: $t("menus.aegisGroupAssets"),
      rank: 2
    },
    children: [
      {
        path: "/cameras/index",
        name: "AegisCameras",
        component: () => import("@/views/cameras/index.vue"),
        meta: {
          icon: "ep/camera",
          title: $t("menus.aegisCameras"),
          roles: ["admin"],
          showParent: true
        }
      },
      {
        path: "/cameras/:id/config",
        name: "AegisCameraConfig",
        component: () => import("@/views/cameras/config.vue"),
        meta: {
          title: $t("menus.aegisCameraConfig"),
          roles: ["admin"],
          showLink: false,
          activePath: "/cameras/index"
        }
      },
      {
        path: "/schedules/index",
        name: "AegisSchedules",
        component: () => import("@/views/schedules/index.vue"),
        meta: {
          icon: "ep/calendar",
          title: $t("menus.aegisSchedules"),
          roles: ["admin"],
          showParent: true
        }
      }
    ]
  },
  {
    path: "/ai",
    component: Layout,
    redirect: "/algorithms/index",
    meta: {
      icon: "ep/cpu",
      title: $t("menus.aegisGroupAI"),
      rank: 3
    },
    children: [
      {
        path: "/algorithms/index",
        name: "AegisAlgorithms",
        component: () => import("@/views/algorithms/index.vue"),
        meta: {
          icon: "ep/operation",
          title: $t("menus.aegisAlgorithms"),
          roles: ["admin"],
          showParent: true
        }
      },
      {
        path: "/datasets/index",
        name: "AegisDatasets",
        component: () => import("@/views/datasets/index.vue"),
        meta: {
          icon: "ep/folder-opened",
          title: $t("menus.aegisDatasets"),
          roles: ["admin", "operator"],
          showParent: true
        }
      },
      {
        path: "/nodes/index",
        name: "AegisNodes",
        component: () => import("@/views/nodes/index.vue"),
        meta: {
          icon: "ep/odometer",
          title: $t("menus.aegisNodes"),
          roles: ["admin"],
          showParent: true
        }
      }
    ]
  },
  {
    path: "/system",
    component: Layout,
    redirect: "/channels/index",
    meta: {
      icon: "ep/setting",
      title: $t("menus.aegisGroupSystem"),
      rank: 4
    },
    children: [
      {
        path: "/channels/index",
        name: "AegisChannels",
        component: () => import("@/views/channels/index.vue"),
        meta: {
          icon: "ep/connection",
          title: $t("menus.aegisChannels"),
          roles: ["admin"],
          showParent: true
        }
      },
      {
        path: "/users/index",
        name: "AegisUsers",
        component: () => import("@/views/users/index.vue"),
        meta: {
          icon: "ep/lock",
          title: $t("menus.aegisUsers"),
          roles: ["admin"],
          showParent: true
        }
      },
      {
        path: "/audit-logs/index",
        name: "AegisAuditLogs",
        component: () => import("@/views/audit-logs/index.vue"),
        meta: {
          icon: "ep/document",
          title: $t("menus.aegisAuditLogs"),
          roles: ["admin"],
          showParent: true
        }
      },
      {
        path: "/settings/index",
        name: "AegisSettings",
        component: () => import("@/views/settings/index.vue"),
        meta: {
          icon: "ep/setting",
          title: $t("menus.aegisSettings"),
          roles: ["admin"],
          showParent: true
        }
      }
    ]
  }
] satisfies Array<RouteConfigsTable>;
