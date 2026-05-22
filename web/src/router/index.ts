import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { auth } from "../stores/auth";

const routes: RouteRecordRaw[] = [
  {
    path: "/login",
    name: "login",
    component: () => import("../views/LoginView.vue"),
  },
  {
    path: "/",
    component: () => import("../layouts/AppLayout.vue"),
    children: [
      { path: "", name: "dashboard", component: () => import("../views/DashboardView.vue") },
      { path: "agents", name: "agents", component: () => import("../views/AgentsView.vue") },
      { path: "numbers", name: "numbers", component: () => import("../views/NumbersView.vue") },
      { path: "calls", name: "calls", component: () => import("../views/CallsView.vue") },
      { path: "webhooks", name: "webhooks", component: () => import("../views/WebhooksView.vue") },
      { path: "settings", name: "settings", component: () => import("../views/SettingsView.vue") },
    ],
  },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

router.beforeEach((to) => {
  if (to.name !== "login" && !auth.isAuthed()) return { name: "login" };
  if (to.name === "login" && auth.isAuthed()) return { name: "dashboard" };
});

export default router;
