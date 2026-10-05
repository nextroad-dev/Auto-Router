<script setup lang="ts">
import { toRef, useId, useTemplateRef } from 'vue'
import JfButton from './JfButton.vue'
import { useOverlayFocus } from '@/lib/overlay'

const props = withDefaults(defineProps<{
  open: boolean
  title: string
  description?: string
  size?: 'sm' | 'md' | 'lg'
  dismissible?: boolean
  /** A destructive confirmation is announced as an alertdialog. */
  role?: 'dialog' | 'alertdialog'
  closeLabel?: string
  /** `confirm` stacks above drawers and other dialogs it may be raised from. */
  layer?: 'modal' | 'confirm'
}>(), {
  description: undefined,
  size: 'md',
  dismissible: true,
  role: 'dialog',
  closeLabel: '关闭',
  layer: 'modal',
})

const emit = defineEmits<{ 'update:open': [boolean] }>()

const headingId = `jf-dialog-${useId()}`
const panel = useTemplateRef<HTMLElement>('panel')

useOverlayFocus({
  open: toRef(props, 'open'),
  panel,
  dismissible: toRef(props, 'dismissible'),
  onEscape: () => emit('update:open', false),
})

function onScrimClick() {
  if (props.dismissible) emit('update:open', false)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="jf-dialog">
      <div v-if="open" class="jf-dialog-root" :data-layer="layer" @click.self="onScrimClick">
        <div class="jf-dialog-scrim" aria-hidden="true" />
        <div
          ref="panel"
          class="jf-dialog-panel"
          :data-size="size"
          :role="role"
          aria-modal="true"
          :aria-labelledby="headingId"
          :aria-describedby="description ? `${headingId}-description` : undefined"
        >
          <header class="jf-dialog-header">
            <div class="jf-dialog-heading">
              <h2 :id="headingId">{{ title }}</h2>
              <p v-if="description" :id="`${headingId}-description`">{{ description }}</p>
            </div>
            <JfButton v-if="dismissible" variant="ghost" square icon="x-mark" :aria-label="closeLabel" @click="emit('update:open', false)" />
          </header>

          <div class="jf-dialog-body">
            <slot />
          </div>

          <footer v-if="$slots.footer" class="jf-dialog-footer">
            <slot name="footer" />
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.jf-dialog-root {
  position: fixed;
  inset: 0;
  z-index: var(--jf-z-modal);
  display: grid;
  place-items: center;
  padding: var(--jf-space-4);
}

.jf-dialog-root[data-layer='confirm'] {
  z-index: calc(var(--jf-z-modal) + 5);
}

.jf-dialog-scrim {
  position: absolute;
  inset: 0;
  background: rgb(0 0 0 / var(--jf-opacity-overlay));
}

.jf-dialog-panel {
  position: relative;
  display: flex;
  width: 100%;
  max-width: var(--jf-form-max);
  max-height: calc(100vh - var(--jf-space-8));
  flex-direction: column;
  border-radius: var(--jf-radius-dialog);
  background: var(--jf-surface);
  box-shadow: var(--jf-shadow-modal);
  padding: var(--jf-space-6);
  overflow: hidden;
}

.jf-dialog-panel[data-size='sm'] {
  max-width: 26rem;
}

.jf-dialog-panel[data-size='lg'] {
  max-width: 48rem;
}

.jf-dialog-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--jf-space-3);
}

.jf-dialog-heading {
  min-width: 0;
  overflow-wrap: anywhere;
}

.jf-dialog-heading h2 {
  font-size: var(--jf-subsection-size);
  line-height: var(--jf-subsection-leading);
  font-weight: var(--jf-emphasis-weight);
  text-wrap: balance;
}

.jf-dialog-heading p {
  margin-top: var(--jf-space-1);
  font-size: var(--jf-caption-size);
  line-height: var(--jf-caption-leading);
  color: var(--jf-text-secondary);
  text-wrap: pretty;
}

.jf-dialog-body {
  min-height: 0;
  margin-top: var(--jf-space-4);
  overflow-y: auto;
  overscroll-behavior: contain;
}

.jf-dialog-footer {
  margin-top: var(--jf-space-5);
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: var(--jf-space-2);
}

.jf-dialog-enter-active {
  transition: opacity var(--jf-duration-overlay) var(--jf-ease-out);
}

.jf-dialog-leave-active {
  transition: opacity var(--jf-duration-exit-overlay) var(--jf-ease-in);
}

.jf-dialog-enter-from,
.jf-dialog-leave-to {
  opacity: 0;
}

.jf-dialog-enter-from .jf-dialog-panel {
  transform: scale(0.96);
}

.jf-dialog-enter-active .jf-dialog-panel,
.jf-dialog-leave-active .jf-dialog-panel {
  transition:
    transform var(--jf-duration-overlay) var(--jf-ease-out),
    opacity var(--jf-duration-overlay) var(--jf-ease-out);
}

.jf-dialog-leave-active .jf-dialog-panel {
  transition-duration: var(--jf-duration-exit-overlay);
  transition-timing-function: var(--jf-ease-in);
}

.jf-dialog-leave-to .jf-dialog-panel {
  transform: scale(0.96);
}
</style>
