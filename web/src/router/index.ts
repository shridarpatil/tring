import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { guardTarget, homeRoute, safeRedirect, type MeUser } from "../lib/roles";

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
          path: "/admin/styleguide",
          name: "styleguide",
          component: () => import("../views/StyleguideView.vue"),
        },
      ]
    : []),
  {
    // Flow editor is full-screen — sits outside AppLayout so it can use
    // the whole viewport for the canvas.
    path: "/admin/flows/:id",
    name: "flow-editor",
    component: () => import("../views/flows/FlowEditorView.vue"),
    meta: { admin: true },
  },
  // Nothing lives at the site root; the login page sends a signed-in user home.
  { path: "/", redirect: { name: "login" } },
  {
    // The agent view: open to every role (admins reach it as "My calls").
    path: "/agent",
    component: () => import("../layouts/AppLayout.vue"),
    children: [
      { path: "", name: "me", component: () => import("../views/MeView.vue") },
      { path: "teams/:name", name: "my-team", component: () => import("../views/MyTeamView.vue") },
    ],
  },
  {
    // The admin console: every child is admin-only (meta.admin).
    path: "/admin",
    component: () => import("../layouts/AppLayout.vue"),
    children: [
      { path: "", name: "dashboard", component: () => import("../views/DashboardView.vue"), meta: { admin: true } },
      { path: "users", name: "users", component: () => import("../views/UsersView.vue"), meta: { admin: true } },
      { path: "agents", name: "agents", redirect: { name: "users" } },
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
  // Routes carry full paths (/login, /agent, /admin/...): the Go binary serves
  // this one SPA at all three. Vite's `base` ("/admin/" in the build) only
  // places the asset bundles, under /admin/assets.
  history: createWebHistory("/"),
  routes,
});

// One /v1/me refresh per page load, shared by concurrent navigations, so role
// or team changes made after login are reconciled with the cached user.
let refreshed: Promise<void> | null = null;

router.beforeEach(async (to) => {
  if (to.name === "styleguide") return; // dev-only, no auth
  if (to.name !== "login" && !auth.isAuthed()) {
    // Remember where they were going (e.g. a shared call link) for after login.
    return safeRedirect(to.fullPath) ? { name: "login", query: { redirect: to.fullPath } } : { name: "login" };
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
