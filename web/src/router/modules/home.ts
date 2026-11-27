import { $t } from "@/plugins/i18n";
const Layout = () => import("@/layout/index.vue");

export default {
  path: "/",
  name: "Home",
  component: Layout,
  redirect: "/dashboard",
  meta: {
    icon: "ep/data-analysis",
    title: $t("menus.aegisDashboard"),
    rank: 0
  },
  children: [
    {
      path: "/dashboard",
      name: "AegisDashboard",
      component: () => import("@/views/dashboard/index.vue"),
      meta: {
        title: $t("menus.aegisDashboard"),
        roles: ["admin", "operator", "viewer"]
      }
    }
  ]
} satisfies RouteConfigsTable;
