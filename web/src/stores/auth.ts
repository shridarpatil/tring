import { reactive } from "vue";

// The console authenticates with the tenant API key (Bearer). The operator
// enters the API base URL + key on the login screen; both persist in
// localStorage so a refresh keeps you signed in. The key never leaves the
// browser except as the Authorization header to the Tring API.

const KEY_BASE = "tring_base_url";
const KEY_API = "tring_api_key";

const state = reactive({
  baseUrl: localStorage.getItem(KEY_BASE) || "http://localhost:8085",
  apiKey: localStorage.getItem(KEY_API) || "",
});

export const auth = {
  state,
  isAuthed: (): boolean => state.apiKey.length > 0,
  creds: () => ({ baseUrl: state.baseUrl, apiKey: state.apiKey }),
  login(baseUrl: string, apiKey: string) {
    state.baseUrl = baseUrl.replace(/\/+$/, "");
    state.apiKey = apiKey.trim();
    localStorage.setItem(KEY_BASE, state.baseUrl);
    localStorage.setItem(KEY_API, state.apiKey);
  },
  logout() {
    state.apiKey = "";
    localStorage.removeItem(KEY_API);
  },
};
