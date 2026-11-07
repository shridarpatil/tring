import { reactive } from "vue";
import { safeRedirect, type MeUser } from "../lib/roles";

// The console authenticates with email + password against
// POST /v1/console/login, which returns a JWT. The JWT is the only
// thing persisted in localStorage; the password never touches storage.
// On refresh the JWT keeps the operator signed in until it expires
// (server-side TTL == 12h); after that, /v1/* returns 401 and the api
// client clears the token, sending the user back to /login.
//
// Same-origin only: the Vue console is embedded in the Go binary (at
// /login, /agent and /admin), so all API calls use relative paths and hit the backend
// that served the bundle. No base-URL field on the login form.

const KEY_TOKEN = "tring_console_token";
const KEY_USER = "tring_console_user";

function loadUser(): MeUser | null {
  try {
    return JSON.parse(localStorage.getItem(KEY_USER) || "null");
  } catch {
    return null;
  }
}

export interface TenantSummary {
  id: string;
  name: string;
  sip_domain: string;
}

/** GET /v1/me: the user plus their tenant. */
export type MeResponse = MeUser & { tenant?: TenantSummary };

const state = reactive({
  token: localStorage.getItem(KEY_TOKEN) || "",
  tenant: null as TenantSummary | null,
  user: loadUser(),
});

export const auth = {
  state,
  isAuthed: (): boolean => state.token.length > 0,
  token: (): string => state.token,
  role: () => state.user?.role ?? null,
  isAdmin: (): boolean => state.user?.role === "admin",
  seesAllTeams: (): boolean => state.user?.role === "admin" || state.user?.role === "supervisor",
  managedTeams: (): string[] => (state.user?.teams ?? []).filter((t) => t.is_manager).map((t) => t.name),
  setSession(token: string, tenant: TenantSummary, user: MeUser) {
    state.token = token;
    state.tenant = tenant;
    state.user = user;
    localStorage.setItem(KEY_TOKEN, token);
    localStorage.setItem(KEY_USER, JSON.stringify(user));
  },
  // Replaces the cached user from GET /v1/me. Covers sessions that predate
  // the cached user and role/team changes made since sign-in. A 401 is
  // handled by the api client (logout); other errors keep the cached copy.
  // The fetch is injected so this module doesn't import the api client
  // (which imports this store).
  // /v1/me also carries the tenant, the only source of its name for
  // non-admins (GET /v1/tenants/me is admin-only) once the page reloads.
  async refreshMe(fetchMe: () => Promise<MeResponse>) {
    try {
      const { tenant, ...u } = await fetchMe();
      if (tenant) state.tenant = tenant;
      state.user = u;
      localStorage.setItem(KEY_USER, JSON.stringify(u));
    } catch {
      /* keep cached user */
    }
  },
  logout() {
    state.token = "";
    state.tenant = null;
    state.user = null;
    localStorage.removeItem(KEY_TOKEN);
    localStorage.removeItem(KEY_USER);
  },
  // signOut ends the session with a full page load of the login page rather
  // than a router push: the agent widget (presence socket, ringing) lives for
  // the page's lifetime and has no teardown, so only a reload stops it.
  // keepPlace sends the user back to the current page after signing in again
  // (an expired session), as the router guard does for a signed-out visit.
  signOut(keepPlace = false) {
    auth.logout();
    const here = location.pathname + location.search;
    const q = keepPlace && safeRedirect(here) ? "?redirect=" + encodeURIComponent(here) : "";
    location.assign("/login" + q);
  },
};
