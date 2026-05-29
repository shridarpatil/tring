<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import { auth } from "../stores/auth";
import { api, ApiError } from "../api/client";

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
  <div class="min-h-screen flex items-center justify-center bg-gray-50 px-4">
    <form
      class="w-full max-w-sm bg-white rounded-xl shadow-sm border p-8 space-y-5"
      @submit.prevent="submit"
    >
      <div class="text-center">
        <div class="text-2xl font-bold">Tring</div>
        <div class="text-sm text-gray-500">Console sign-in</div>
      </div>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Email</span>
        <input
          v-model="email"
          type="email"
          required
          autocomplete="username"
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-gray-900"
          placeholder="admin@tring.local"
        />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">Password</span>
        <input
          v-model="password"
          type="password"
          required
          autocomplete="current-password"
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-gray-900"
        />
      </label>
      <p v-if="error" class="text-sm text-red-600">{{ error }}</p>
      <button
        type="submit"
        :disabled="loading"
        class="w-full bg-gray-900 text-white rounded-md py-2 text-sm font-medium hover:bg-gray-800 disabled:opacity-50"
      >
        {{ loading ? "Signing in…" : "Sign in" }}
      </button>
    </form>
  </div>
</template>
