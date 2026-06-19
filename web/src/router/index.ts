import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { auth } from "../stores/auth";

const routes: RouteRecordRaw[] = [
  {
    path: "/login",
    name: "login",
    component: () => import("../views/LoginView.vue"),
  },
  // Living component gallery — dev-only, tree-shaken out of the embedded
  // production build via the import.meta.env.DEV guard.
  ...(import.meta.env.DEV
    ? [
        {
          path: "/styleguide",
          name: "styleguide",
          component: () => import("../views/StyleguideView.vue"),
        },
      ]
    : []),
  {
    // Flow editor is full-screen — sits outside AppLayout so it can use
    // the whole viewport for the canvas.
    path: "/flows/:id",
    name: "flow-editor",
    component: () => import("../views/flows/FlowEditorView.vue"),
  },
  {
    path: "/",
    component: () => import("../layouts/AppLayout.vue"),
    children: [
      { path: "", name: "dashboard", component: () => import("../views/DashboardView.vue") },
      { path: "agents", name: "agents", component: () => import("../views/AgentsView.vue") },
      { path: "teams", name: "teams", component: () => import("../views/TeamsView.vue") },
      { path: "sounds", name: "sounds", component: () => import("../views/SoundsView.vue") },
      { path: "numbers", name: "numbers", component: () => import("../views/NumbersView.vue") },
      { path: "calls", name: "calls", component: () => import("../views/CallsView.vue") },
      { path: "webhooks", name: "webhooks", component: () => import("../views/WebhooksView.vue") },
      { path: "api-keys", name: "api-keys", component: () => import("../views/ApiKeysView.vue") },
      { path: "flows", name: "flows", component: () => import("../views/flows/FlowsListView.vue") },
      { path: "trunks", name: "trunks", component: () => import("../views/TrunksView.vue") },
      { path: "settings", name: "settings", component: () => import("../views/SettingsView.vue") },
    ],
  },
];

const router = createRouter({
  // Vite injects BASE_URL from vite.config.ts's `base` option. In the
  // embedded build that's "/admin/" (so the embedded UI mounts under
  // /admin/* in the Go binary). For host-only `npm run dev` it's "/".
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
});

router.beforeEach((to) => {
  if (to.name === "styleguide") return; // dev-only, no auth
  if (to.name !== "login" && !auth.isAuthed()) return { name: "login" };
  if (to.name === "login" && auth.isAuthed()) return { name: "dashboard" };
});

export default router;
