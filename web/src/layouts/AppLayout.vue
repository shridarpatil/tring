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
  <div class="app">
    <aside class="sidebar">
      <div class="brand">
        <span class="brand-dot"></span>
        <span class="brand-name">Tring</span>
      </div>
      <SidebarNav :items="nav" />
      <div class="sidebar-foot">v0.1 console</div>
    </aside>

    <div class="main-col">
      <header class="topbar">
        <LiveStatusStrip />
        <div class="topbar-right">
          <div class="tenant">
            <span>{{ tenant?.name || "…" }}</span>
            <span v-if="tenant" class="sep">·</span>
            <span v-if="tenant" class="sip">{{ tenant.sip_domain }}</span>
          </div>
          <IconButton
            :title="theme.state.theme === 'dark' ? 'Switch to light' : 'Switch to dark'"
            size="sm"
            @click="theme.toggle()"
          >
            <component :is="theme.state.theme === 'dark' ? Sun : Moon" :size="16" />
          </IconButton>
          <button type="button" class="signout" @click="logout">Sign out</button>
        </div>
      </header>
      <main class="content"><RouterView /></main>
    </div>
    <Toaster />
  </div>
</template>

<style scoped>
.app { min-height: 100vh; display: flex; background: var(--background); color: var(--foreground); }
.sidebar {
  width: 14rem;
  flex-shrink: 0;
  background: var(--card);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
}
.brand {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0 1.25rem;
  height: 3.5rem;
  border-bottom: 1px solid var(--border);
}
.brand-dot { height: 0.5rem; width: 0.5rem; border-radius: 9999px; background: var(--primary); }
.brand-name { font-size: 1.125rem; font-weight: 600; letter-spacing: -0.02em; }
.sidebar-foot {
  padding: 0.75rem 1.25rem;
  font-size: var(--text-8);
  font-family: var(--font-mono);
  color: var(--faint-foreground);
  border-top: 1px solid var(--border);
}
.main-col { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.topbar {
  height: 3.5rem;
  flex-shrink: 0;
  background: var(--card);
  border-bottom: 1px solid var(--border);
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1.5rem;
  padding: 0 1.5rem;
}
.topbar-right { display: flex; align-items: center; gap: 1rem; min-width: 0; }
.tenant { font-size: var(--text-7); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tenant .sep { color: var(--faint-foreground); margin: 0 0.375rem; }
.tenant .sip { font-family: var(--font-mono); font-size: var(--text-8); color: var(--muted-foreground); }
.signout {
  background: transparent;
  border: none;
  padding: 0;
  font-size: var(--text-7);
  color: var(--muted-foreground);
  cursor: pointer;
  transition: color var(--transition-fast);
}
.signout:hover { color: var(--foreground); }
.content { flex: 1; padding: 1.5rem; overflow: auto; }
</style>
