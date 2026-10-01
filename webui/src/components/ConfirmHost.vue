<script setup lang="ts">
import JfButton from './JfButton.vue'
import JfDialog from './JfDialog.vue'
import { confirmState, resolveConfirm } from '@/lib/confirm'
</script>

<template>
  <JfDialog
    :open="confirmState.open"
    :title="confirmState.title"
    :description="confirmState.description"
    :role="confirmState.danger ? 'alertdialog' : 'dialog'"
    size="sm"
    layer="confirm"
    @update:open="open => { if (!open) resolveConfirm(false) }"
  >
    <template #footer>
      <div class="jf-action-group justify-end">
        <JfButton variant="ghost" @click="resolveConfirm(false)">取消</JfButton>
        <JfButton :variant="confirmState.danger ? 'danger' : 'primary'" @click="resolveConfirm(true)">
          {{ confirmState.confirmLabel }}
        </JfButton>
      </div>
    </template>
  </JfDialog>
</template>
