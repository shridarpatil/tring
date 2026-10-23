<script setup lang="ts">
import Modal from "./Modal.vue";
import Button from "./Button.vue";
import { confirmState, settleConfirm } from "../../lib/confirm";
</script>

<template>
  <!-- Escape / backdrop emit update:open=false, which settles as "cancel". -->
  <Modal
    :open="confirmState.open"
    :title="confirmState.title"
    size="sm"
    @update:open="(v) => !v && settleConfirm(false)"
  >
    <p>{{ confirmState.message }}</p>
    <template #footer>
      <Button variant="secondary" @click="settleConfirm(false)">Cancel</Button>
      <Button variant="danger" @click="settleConfirm(true)">{{ confirmState.confirmLabel }}</Button>
    </template>
  </Modal>
</template>
