import { reactive } from "vue";

// The console authenticates with email + password against
// POST /v1/console/login, which returns a JWT. The JWT is the only
// thing persisted in localStorage; the password never touches storage.
// On refresh the JWT keeps the operator signed in until it expires
// (server-side TTL == 12h); after that, /v1/* returns 401 and the api
// client clears the token, sending the user back to /login.
//
// Same-origin only: the Vue console is embedded in the Go binary at
// /admin/, so all API calls use relative paths and hit the backend
// that served the bundle. No base-URL field on the login form.

const KEY_TOKEN = "tring_console_token";

interface TenantSummary {
  id: string;
  name: string;
  sip_domain: string;
}

const state = reactive({
  token: localStorage.getItem(KEY_TOKEN) || "",
  tenant: null as TenantSummary | null,
});

export const auth = {
  state,
  isAuthed: (): boolean => state.token.length > 0,
  token: (): string => state.token,
  setSession(token: string, tenant: TenantSummary) {
    state.token = token;
    state.tenant = tenant;
    localStorage.setItem(KEY_TOKEN, token);
  },
  logout() {
    state.token = "";
    state.tenant = null;
    localStorage.removeItem(KEY_TOKEN);
  },
};
