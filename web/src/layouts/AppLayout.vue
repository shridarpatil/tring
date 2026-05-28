<script setup lang="ts">
import { ref, onMounted } from "vue";
import { useRouter, useRoute } from "vue-router";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import type { Tenant } from "../api/types";

const router = useRouter();
const route = useRoute();
const tenant = ref<Tenant | null>(null);

const nav = [
  { to: "/", label: "Dashboard" },
  { to: "/agents", label: "Agents" },
  { to: "/numbers", label: "Numbers" },
  { to: "/trunks", label: "Trunks" },
  { to: "/calls", label: "Calls" },
  { to: "/flows", label: "IVR Flows" },
  { to: "/webhooks", label: "Webhooks" },
  { to: "/settings", label: "Settings" },
];

function isActive(to: string): boolean {
  return to === "/" ? route.path === "/" : route.path.startsWith(to);
}

onMounted(async () => {
  try {
    tenant.value = await api.get<Tenant>("/v1/tenants/me");
  } catch {
    /* 401 handled by the client + guard */
  }
});

function logout() {
  auth.logout();
  router.push({ name: "login" });
}
</script>

<template>
  <div class="min-h-screen flex bg-gray-50 text-gray-900">
    <aside class="w-56 shrink-0 bg-gray-900 text-gray-100 flex flex-col">
      <div class="px-5 py-4 text-xl font-bold tracking-tight border-b border-gray-800">
        Tring
      </div>
      <nav class="flex-1 px-2 py-3 space-y-1">
        <RouterLink
          v-for="item in nav"
          :key="item.to"
          :to="item.to"
          :class="[
            'block px-3 py-2 rounded-md text-sm hover:bg-gray-800 transition-colors',
            isActive(item.to) ? 'bg-gray-800 font-medium' : 'text-gray-300',
          ]"
          >{{ item.label }}</RouterLink
        >
      </nav>
      <div class="px-4 py-3 text-xs text-gray-500 border-t border-gray-800">
        v0.1 console
      </div>
    </aside>

    <div class="flex-1 flex flex-col min-w-0">
      <header class="h-14 bg-white border-b flex items-center justify-between px-6">
        <div class="text-sm text-gray-500">
          {{ tenant?.name || "…" }}
          <span v-if="tenant" class="text-gray-300">·</span>
          <span v-if="tenant" class="text-gray-400">{{ tenant.sip_domain }}</span>
        </div>
        <button class="text-sm text-gray-600 hover:text-gray-900" @click="logout">
          Sign out
        </button>
      </header>
      <main class="flex-1 p-6 overflow-auto">
        <RouterView />
      </main>
    </div>
  </div>
</template>
