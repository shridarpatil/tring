import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { guardTarget, homeRoute, type MeUser } from "../lib/roles";

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
    meta: { admin: true },
  },
  {
    path: "/",
    component: () => import("../layouts/AppLayout.vue"),
    children: [
      // Open to every role; the rest of the children are admin-only (meta.admin).
      { path: "me", name: "me", component: () => import("../views/MeView.vue") },
      { path: "me/teams/:name", name: "my-team", component: () => import("../views/MyTeamView.vue") },
      { path: "", name: "dashboard", component: () => import("../views/DashboardView.vue"), meta: { admin: true } },
      { path: "agents", name: "agents", component: () => import("../views/AgentsView.vue"), meta: { admin: true } },
      { path: "teams", name: "teams", component: () => import("../views/TeamsView.vue"), meta: { admin: true } },
      { path: "teams/:name", name: "team", component: () => import("../views/TeamDetailView.vue"), meta: { admin: true } },
      { path: "sounds", name: "sounds", component: () => import("../views/SoundsView.vue"), meta: { admin: true } },
      { path: "numbers", name: "numbers", component: () => import("../views/NumbersView.vue"), meta: { admin: true } },
      // calls/:id opens that call's detail (?view=log → full log), so a call
      // has a URL that can be shared. Optional param = same view instance, so
      // the list's page/filter survive opening and closing a call.
      { path: "calls/:id?", name: "calls", component: () => import("../views/CallsView.vue"), meta: { admin: true } },
      { path: "webhooks", name: "webhooks", component: () => import("../views/WebhooksView.vue"), meta: { admin: true } },
      { path: "api-keys", name: "api-keys", component: () => import("../views/ApiKeysView.vue"), meta: { admin: true } },
      { path: "flows", name: "flows", component: () => import("../views/flows/FlowsListView.vue"), meta: { admin: true } },
      { path: "trunks", name: "trunks", component: () => import("../views/TrunksView.vue"), meta: { admin: true } },
      { path: "settings", name: "settings", component: () => import("../views/SettingsView.vue"), meta: { admin: true } },
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

// One /v1/me refresh per page load, shared by concurrent navigations, so role
// or team changes made after login are reconciled with the cached user.
let refreshed: Promise<void> | null = null;

router.beforeEach(async (to) => {
  if (to.name === "styleguide") return; // dev-only, no auth
  if (to.name !== "login" && !auth.isAuthed()) {
    // Remember where they were going (e.g. a shared call link) for after login.
    return to.fullPath === "/" ? { name: "login" } : { name: "login", query: { redirect: to.fullPath } };
  }
  if (!auth.isAuthed()) return; // login page, signed out: nothing to refresh
  refreshed ??= auth.refreshMe(() => api.get<MeUser>("/v1/me"));
  await refreshed;
  if (!auth.isAuthed()) return { name: "login" }; // 401 -> client logged out
  const u = auth.state.user;
  if (to.name === "login" && auth.isAuthed()) return { name: u ? homeRoute(u) : "dashboard" };
  if (to.matched.some((r) => guardTarget(u, r) === "me")) return { name: "me" };
});

export default router;
