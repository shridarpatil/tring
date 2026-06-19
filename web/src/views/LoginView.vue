<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import { auth } from "../stores/auth";
import { api, ApiError } from "../api/client";
import { Button, TextField } from "../components/ui";

interface LoginResponse {
  token: string;
  expires_at: string;
  tenant: { id: string; name: string; sip_domain: string };
}

const router = useRouter();
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
    router.push({ name: "dashboard" });
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : "sign-in failed";
  } finally {
    loading.value = false;
  }
}
</script>

<template>
  <div class="min-h-screen flex items-center justify-center bg-base px-4">
    <form
      class="w-full max-w-sm bg-surface rounded-xl border border-border p-8 space-y-5 shadow-2xl"
      @submit.prevent="submit"
    >
      <div class="text-center space-y-1">
        <div class="flex items-center justify-center gap-2">
          <span class="h-2 w-2 rounded-full bg-accent"></span>
          <span class="text-2xl font-semibold tracking-tight">Tring</span>
        </div>
        <div class="text-sm text-ink-muted">Console sign-in</div>
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
      <p v-if="error" class="text-sm text-danger">{{ error }}</p>
      <Button type="submit" :loading="loading" block>
        {{ loading ? "Signing in…" : "Sign in" }}
      </Button>
    </form>
  </div>
</template>
