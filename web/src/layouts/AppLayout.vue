<script setup lang="ts">
import { ref, onMounted } from "vue";
import { useRouter } from "vue-router";
import {
  LayoutDashboard,
  Headset,
  UsersRound,
  Music,
  Hash,
  Cable,
  PhoneCall,
  Workflow,
  Webhook,
  KeyRound,
  Settings,
  Sun,
  Moon,
} from "lucide-vue-next";
import { api } from "../api/client";
import { auth } from "../stores/auth";
import { theme } from "../lib/theme";
import type { Tenant } from "../api/types";
import SidebarNav from "../components/shell/SidebarNav.vue";
import LiveStatusStrip from "../components/shell/LiveStatusStrip.vue";
import IconButton from "../components/ui/IconButton.vue";
import Toaster from "../components/ui/Toaster.vue";

const router = useRouter();
const tenant = ref<Tenant | null>(null);

const nav = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard },
  { to: "/agents", label: "Agents", icon: Headset },
  { to: "/teams", label: "Teams", icon: UsersRound },
  { to: "/sounds", label: "Sounds", icon: Music },
  { to: "/numbers", label: "Numbers", icon: Hash },
  { to: "/trunks", label: "Trunks", icon: Cable },
  { to: "/calls", label: "Calls", icon: PhoneCall },
  { to: "/flows", label: "IVR Flows", icon: Workflow },
  { to: "/webhooks", label: "Webhooks", icon: Webhook },
  { to: "/api-keys", label: "API keys", icon: KeyRound },
  { to: "/settings", label: "Settings", icon: Settings },
];

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
  <div class="min-h-screen flex bg-base text-ink">
    <aside class="w-56 shrink-0 bg-surface border-r border-border flex flex-col">
      <div class="flex items-center gap-2 px-5 h-14 border-b border-border">
        <span class="h-2 w-2 rounded-full bg-accent"></span>
        <span class="text-lg font-semibold tracking-tight">Tring</span>
      </div>
      <SidebarNav :items="nav" />
      <div class="px-5 py-3 text-xs font-mono text-ink-faint border-t border-border">
        v0.1 console
      </div>
    </aside>

    <div class="flex-1 flex flex-col min-w-0">
      <header
        class="h-14 shrink-0 bg-surface border-b border-border flex items-center justify-between gap-6 px-6"
      >
        <LiveStatusStrip />
        <div class="flex items-center gap-4 min-w-0">
          <div class="text-sm truncate">
            <span class="text-ink">{{ tenant?.name || "…" }}</span>
            <span v-if="tenant" class="text-ink-faint mx-1.5">·</span>
            <span v-if="tenant" class="font-mono text-xs text-ink-muted">{{
              tenant.sip_domain
            }}</span>
          </div>
          <IconButton
            :title="theme.state.theme === 'dark' ? 'Switch to light' : 'Switch to dark'"
            size="sm"
            @click="theme.toggle()"
          >
            <component :is="theme.state.theme === 'dark' ? Sun : Moon" class="h-4 w-4" />
          </IconButton>
          <button
            class="text-sm text-ink-muted hover:text-ink transition-colors"
            @click="logout"
          >
            Sign out
          </button>
        </div>
      </header>
      <main class="flex-1 p-6 overflow-auto">
        <RouterView />
      </main>
    </div>
    <Toaster />
  </div>
</template>
