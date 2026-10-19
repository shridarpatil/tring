<script setup lang="ts">
import { ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { auth } from "../stores/auth";
import { api, ApiError } from "../api/client";
import { Button, TextField } from "../components/ui";

interface LoginResponse {
  token: string;
  expires_at: string;
  tenant: { id: string; name: string; sip_domain: string };
}

const router = useRouter();
const route = useRoute();

// Only same-app paths: "/calls/…" yes; "//evil.example" or "https://…" no.
function safeRedirect(): string | null {
  const r = route.query.redirect;
  return typeof r === "string" && r.startsWith("/") && !r.startsWith("//") ? r : null;
}
const email = ref("");
const password = ref("");
const error = ref("");
const loading = ref(false);

async function submit() {
  error.value = "";
  loading.value = true;
  try {
    const res = await api.post<LoginResponse>("/v1/console/login", {
      email: email.value.trim(),
      password: password.value,
    });
    auth.setSession(res.token, res.tenant);
    const next = safeRedirect();
    if (next) router.replace(next);
    else router.push({ name: "dashboard" });
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : "sign-in failed";
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <div class="login">
    <form class="card login-card" @submit.prevent="submit">
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
        v-model="password"
        type="password"
        label="Password"
        required
        autocomplete="current-password"
      />
      <p v-if="error" class="login-error">{{ error }}</p>
      <Button type="submit" :loading="loading" block>
        {{ loading ? "Signing in…" : "Sign in" }}
      </Button>
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
