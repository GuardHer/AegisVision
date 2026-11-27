import { $t } from "@/plugins/i18n";
const Layout = () => import("@/layout/index.vue");

export default [
  {
    path: "/monitor",
    component: Layout,
    redirect: "/monitor/index",
    meta: {
      icon: "ep/video-camera",
      title: $t("menus.aegisMonitor"),
      rank: 1
    },
    children: [
      {
        path: "/monitor/index",
        name: "AegisMonitor",
        component: () => import("@/views/monitor/index.vue"),
        meta: {
          title: $t("menus.aegisMonitor"),
          roles: ["admin", "operator", "viewer"]
        }
      }
    ]
  },
  {
    path: "/alarms",
    component: Layout,
    redirect: "/alarms/index",
    meta: {
      icon: "ep/bell",
      title: $t("menus.aegisAlarms"),
      rank: 2
    },
    children: [
      {
        path: "/alarms/index",
        name: "AegisAlarms",
        component: () => import("@/views/alarms/index.vue"),
        meta: {
          title: $t("menus.aegisAlarms"),
          roles: ["admin", "operator", "viewer"]
        }
      }
    ]
  },
  {
    path: "/analytics",
    component: Layout,
    redirect: "/analytics/index",
    meta: {
      icon: "ep/data-analysis",
      title: $t("menus.aegisAnalytics"),
      rank: 3
    },
    children: [
      {
        path: "/analytics/index",
        name: "AegisAnalytics",
        component: () => import("@/views/analytics/index.vue"),
        meta: {
          title: $t("menus.aegisAnalytics"),
          roles: ["admin", "operator", "viewer"]
        }
      }
    ]
  },
  {
    path: "/cameras",
    component: Layout,
    redirect: "/cameras/index",
    meta: {
      icon: "ep/camera",
      title: $t("menus.aegisCameras"),
      rank: 4
    },
    children: [
      {
        path: "/cameras/index",
        name: "AegisCameras",
        component: () => import("@/views/cameras/index.vue"),
        meta: {
          title: $t("menus.aegisCameras"),
          roles: ["admin"]
        }
      },
      {
        path: "/cameras/:id/config",
        name: "AegisCameraConfig",
        component: () => import("@/views/cameras/config.vue"),
        meta: {
          title: $t("menus.aegisCameraConfig"),
          roles: ["admin"],
          showLink: false
        }
      }
    ]
  },
  {
    path: "/schedules",
    component: Layout,
    redirect: "/schedules/index",
    meta: {
      icon: "ep/calendar",
      title: $t("menus.aegisSchedules"),
      rank: 5
    },
    children: [
      {
        path: "/schedules/index",
        name: "AegisSchedules",
        component: () => import("@/views/schedules/index.vue"),
        meta: {
          title: $t("menus.aegisSchedules"),
          roles: ["admin"]
        }
      }
    ]
  },
  {
    path: "/algorithms",
    component: Layout,
    redirect: "/algorithms/index",
    meta: {
      icon: "ep/cpu",
      title: $t("menus.aegisAlgorithms"),
      rank: 6
    },
    children: [
      {
        path: "/algorithms/index",
        name: "AegisAlgorithms",
        component: () => import("@/views/algorithms/index.vue"),
        meta: {
          title: $t("menus.aegisAlgorithms"),
          roles: ["admin"]
        }
      }
    ]
  },
  {
    path: "/nodes",
    component: Layout,
    redirect: "/nodes/index",
    meta: {
      icon: "ep/monitor",
      title: $t("menus.aegisNodes"),
      rank: 7
    },
    children: [
      {
        path: "/nodes/index",
        name: "AegisNodes",
        component: () => import("@/views/nodes/index.vue"),
        meta: {
          title: $t("menus.aegisNodes"),
          roles: ["admin"]
        }
      }
    ]
  },
  {
    path: "/channels",
    component: Layout,
    redirect: "/channels/index",
    meta: {
      icon: "ep/connection",
      title: $t("menus.aegisChannels"),
      rank: 8
    },
    children: [
      {
        path: "/channels/index",
        name: "AegisChannels",
        component: () => import("@/views/channels/index.vue"),
        meta: {
          title: $t("menus.aegisChannels"),
          roles: ["admin"]
        }
      }
    ]
  },
  {
    path: "/users",
    component: Layout,
    redirect: "/users/index",
    meta: {
      icon: "ep/user",
      title: $t("menus.aegisUsers"),
      rank: 9
    },
    children: [
      {
        path: "/users/index",
        name: "AegisUsers",
        component: () => import("@/views/users/index.vue"),
        meta: {
          title: $t("menus.aegisUsers"),
          roles: ["admin"]
        }
      }
    ]
  },
  {
    path: "/datasets",
    component: Layout,
    redirect: "/datasets/index",
    meta: {
      icon: "ep/folder-opened",
      title: $t("menus.aegisDatasets"),
      rank: 10
    },
    children: [
      {
        path: "/datasets/index",
        name: "AegisDatasets",
        component: () => import("@/views/datasets/index.vue"),
        meta: {
          title: $t("menus.aegisDatasets"),
          roles: ["admin", "operator"]
        }
      }
    ]
  },
  {
    path: "/audit-logs",
    component: Layout,
    redirect: "/audit-logs/index",
    meta: {
      icon: "ep/document",
      title: $t("menus.aegisAuditLogs"),
      rank: 11
    },
    children: [
      {
        path: "/audit-logs/index",
        name: "AegisAuditLogs",
        component: () => import("@/views/audit-logs/index.vue"),
        meta: {
          title: $t("menus.aegisAuditLogs"),
          roles: ["admin"]
        }
      }
    ]
  },
  {
    path: "/settings",
    component: Layout,
    redirect: "/settings/index",
    meta: {
      icon: "ep/setting",
      title: $t("menus.aegisSettings"),
      rank: 12
    },
    children: [
      {
        path: "/settings/index",
        name: "AegisSettings",
        component: () => import("@/views/settings/index.vue"),
        meta: {
          title: $t("menus.aegisSettings"),
          roles: ["admin"]
        }
      }
    ]
  }
] satisfies Array<RouteConfigsTable>;
