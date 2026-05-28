<script setup lang="ts">
import { ref } from "vue";
import { useRouter } from "vue-router";
import { auth } from "../stores/auth";
import { api } from "../api/client";

const router = useRouter();
const baseUrl = ref(auth.state.baseUrl);
const apiKey = ref("");
const error = ref("");
const loading = ref(false);

async function submit() {
  error.value = "";
  loading.value = true;
  auth.login(baseUrl.value, apiKey.value);
  try {
    await api.get("/v1/tenants/me"); // validates the key
    router.push({ name: "dashboard" });
  } catch (e) {
    auth.logout();
    error.value = e instanceof Error ? e.message : "sign-in failed";
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
        <span class="text-sm font-medium text-gray-700">API base URL</span>
        <input
          v-model="baseUrl"
          type="url"
          required
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-gray-900"
          placeholder="http://localhost:8085"
        />
      </label>
      <label class="block">
        <span class="text-sm font-medium text-gray-700">API key</span>
        <input
          v-model="apiKey"
          type="password"
          required
          class="mt-1 w-full rounded-md border border-gray-300 px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-gray-900"
          placeholder="tk_live_…"
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
      <p class="text-xs text-gray-400 text-center">
        The key is stored in your browser and sent only to the Tring API.
      </p>
    </form>
  </div>
</template>
