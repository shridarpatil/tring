<script setup lang="ts">
withDefaults(defineProps<{ padded?: boolean }>(), { padded: true });
</script>

<template>
  <div class="rounded-xl bg-surface border border-border" :class="padded ? 'p-5' : ''">
    <slot />
  </div>
</template>
