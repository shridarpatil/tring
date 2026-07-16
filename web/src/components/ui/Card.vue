<script setup lang="ts">
withDefaults(defineProps<{ padded?: boolean }>(), { padded: true });
</script>

<template>
  <div class="card" :style="padded ? undefined : { padding: '0' }">
    <slot />
  </div>
</template>
