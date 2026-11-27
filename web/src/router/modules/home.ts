import { $t } from "@/plugins/i18n";
const Layout = () => import("@/layout/index.vue");

export default {
  path: "/",
  name: "Home",
  component: Layout,
  redirect: "/monitor",
  meta: {
    icon: "ep/video-camera",
    title: $t("menus.aegisMonitor"),
    rank: 0
  },
  children: [
    {
      path: "/monitor",
      name: "AegisMonitor",
      component: () => import("@/views/monitor/index.vue"),
      meta: {
        title: $t("menus.aegisMonitor"),
        roles: ["admin", "operator", "viewer"]
      }
    }
  ]
} satisfies RouteConfigsTable;
