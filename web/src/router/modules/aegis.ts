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
    path: "/cameras",
    component: Layout,
    redirect: "/cameras/index",
    meta: {
      icon: "ep/camera",
      title: $t("menus.aegisCameras"),
      rank: 3
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
    path: "/algorithms",
    component: Layout,
    redirect: "/algorithms/index",
    meta: {
      icon: "ep/cpu",
      title: $t("menus.aegisAlgorithms"),
      rank: 4
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
      rank: 5
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
      rank: 6
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
      rank: 7
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
  }
] satisfies Array<RouteConfigsTable>;
