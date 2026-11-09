<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { auth } from "../stores/auth";
import { api, ApiError } from "../api/client";
import { homeRoute, safeRedirect, type MeUser } from "../lib/roles";
import { parseSsoFragment, ssoErrorMessage } from "../lib/sso";
import { Button, TextField } from "../components/ui";

interface LoginResponse {
  token: string;
  expires_at: string;
  tenant: { id: string; name: string; sip_domain: string };
  user: MeUser;
}

const router = useRouter();
const route = useRoute();

const email = ref("");
const password = ref("");
const error = ref("");
const loading = ref(false);
// Step 1 asks only for the email; the server says whether to hand off to the
// IdP or ask for a password.
const showPassword = ref(false);

function finish(res: LoginResponse, redirect: unknown) {
  auth.setSession(res.token, res.tenant, res.user);
  const next = safeRedirect(redirect);
  if (next) router.replace(next);
  else router.push({ name: homeRoute(res.user) });
}

async function start() {
  error.value = "";
  loading.value = true;
  try {
    const redirect = route.query.redirect;
    const res = await api.post<{ method: string; redirect_url?: string }>("/v1/console/login/start", {
      email: email.value.trim(),
      redirect: typeof redirect === "string" ? redirect : undefined,
    });
    if (res.method === "sso" && res.redirect_url) {
      location.assign(res.redirect_url);
      return; // keep the spinner while the browser navigates away
    }
  } catch (e) {
    // Only a rate limit is worth showing; any other failure falls back to the
    // password field so admins on dotless domains (admin@localhost) can sign in.
    if (e instanceof ApiError && e.status === 429) error.value = e.message;
  }
  showPassword.value = true;
  loading.value = false;
}

async function submit() {
  error.value = "";
  loading.value = true;
  try {
    const res = await api.post<LoginResponse>("/v1/console/login", {
      email: email.value.trim(),
      password: password.value,
    });
    finish(res, route.query.redirect);
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : "sign-in failed";
  } finally {
    loading.value = false;
  }
}

onMounted(async () => {
  // The code and redirect arrive in the URL fragment (never sent to servers or
  // logs); failures arrive as ?sso_error=. Clear both so neither lingers in the
  // address bar or history.
  const frag = parseSsoFragment(location.hash);
  const err = ssoErrorMessage(route.query.sso_error);
  if (!frag && !err) return;
  history.replaceState(null, "", "/login");
  if (err) {
    error.value = err;
    return;
  }
  if (!frag) return;
  loading.value = true;
  try {
    const res = await api.post<LoginResponse>("/v1/console/sso/exchange", { code: frag.code });
    finish(res, frag.redirect);
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : "Single sign-on failed";
    loading.value = false;
  }
});
</script>

<template>
  <div class="login">
    <form class="card login-card" @submit.prevent="showPassword ? submit() : start()">
      <div class="login-brand">
        <div class="login-word">
          <span class="brand-dot"></span>
          <span class="brand-name">Tring</span>
        </div>
        <div class="text-light">Console sign-in</div>
      </div>
      <TextField
        v-model="email"
        type="email"
        label="Email"
        required
        autocomplete="username"
        placeholder="admin@tring.local"
      />
      <TextField
        v-if="showPassword"
        v-model="password"
        type="password"
        label="Password"
        required
        autocomplete="current-password"
      />
      <p v-if="error" class="login-error">{{ error }}</p>
      <Button type="submit" :loading="loading" block>
        {{ showPassword ? (loading ? "Signing in…" : "Sign in") : loading ? "Checking…" : "Continue" }}
      </Button>
      <button v-if="!showPassword" type="button" class="ghost small" @click="showPassword = true">
        Sign in with password
      </button>
    </form>
  </div>
</template>

<style scoped>
.login {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--background);
  padding: 1rem;
}
.login-card {
  width: 100%;
  max-width: 24rem;
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
  padding: 2rem;
}
.login-brand { text-align: center; display: flex; flex-direction: column; gap: 0.25rem; }
.login-word { display: flex; align-items: center; justify-content: center; gap: 0.5rem; }
.brand-dot { height: 0.5rem; width: 0.5rem; border-radius: 9999px; background: var(--primary); }
.brand-name { font-size: 1.5rem; font-weight: 600; letter-spacing: -0.02em; }
.login-error { font-size: var(--text-7); color: var(--danger); }
</style>
