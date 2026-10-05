<script setup lang="ts">
import { toRef, useId, useTemplateRef } from 'vue'
import JfButton from './JfButton.vue'
import { useOverlayFocus } from '@/lib/overlay'

const props = withDefaults(defineProps<{
  open: boolean
  title: string
  description?: string
  size?: 'sm' | 'md' | 'lg'
  /** False when closing without acknowledging the content would lose information. */
  dismissible?: boolean
  closeLabel?: string
}>(), {
  description: undefined,
  size: 'md',
  dismissible: true,
  closeLabel: '关闭',
})

const emit = defineEmits<{ 'update:open': [boolean] }>()

const headingId = `jf-drawer-${useId()}`
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
    <Transition name="jf-drawer">
      <div v-if="open" class="jf-drawer-root">
        <div class="jf-drawer-scrim" @click="onScrimClick" />
        <div
          ref="panel"
          class="jf-drawer-panel"
          :data-size="size"
          role="dialog"
          aria-modal="true"
          :aria-labelledby="headingId"
          :aria-describedby="description ? `${headingId}-description` : undefined"
        >
          <header class="jf-drawer-header">
            <div class="jf-drawer-heading">
              <h2 :id="headingId">{{ title }}</h2>
              <p v-if="description" :id="`${headingId}-description`">{{ description }}</p>
            </div>
            <JfButton variant="ghost" square icon="x-mark" :aria-label="closeLabel" @click="emit('update:open', false)" />
          </header>

          <div class="jf-drawer-body">
            <slot />
          </div>

          <footer v-if="$slots.footer" class="jf-drawer-footer">
            <slot name="footer" />
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.jf-drawer-root {
  position: fixed;
  inset: 0;
  z-index: var(--jf-z-modal);
  display: flex;
  justify-content: flex-end;
}

.jf-drawer-scrim {
  position: absolute;
  inset: 0;
  background: rgb(0 0 0 / var(--jf-opacity-overlay));
}

.jf-drawer-panel {
  position: relative;
  display: flex;
  width: 100%;
  max-width: 34rem;
  flex-direction: column;
  border-radius: var(--jf-radius-dialog) 0 0 var(--jf-radius-dialog);
  background: var(--jf-surface);
  box-shadow: var(--jf-shadow-drawer);
  overflow: hidden;
}

.jf-drawer-panel[data-size='sm'] {
  max-width: 26rem;
}

.jf-drawer-panel[data-size='lg'] {
  max-width: 48rem;
}

.jf-drawer-header {
  display: flex;
  flex-shrink: 0;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--jf-space-3);
  padding: var(--jf-space-5) var(--jf-space-6) 0;
}

.jf-drawer-heading {
  min-width: 0;
  overflow-wrap: anywhere;
}

.jf-drawer-heading h2 {
  font-size: var(--jf-subsection-size);
  line-height: var(--jf-subsection-leading);
  font-weight: var(--jf-emphasis-weight);
  text-wrap: balance;
}

.jf-drawer-heading p {
  margin-top: var(--jf-space-1);
  font-size: var(--jf-caption-size);
  line-height: var(--jf-caption-leading);
  color: var(--jf-text-secondary);
  text-wrap: pretty;
}

/* Long content scrolls inside the panel; the header and actions stay reachable. */
.jf-drawer-body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--jf-space-6);
  overscroll-behavior: contain;
}

.jf-drawer-footer {
  flex-shrink: 0;
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  align-items: center;
  gap: var(--jf-space-2);
  padding: 0 var(--jf-space-6) var(--jf-space-6);
}

.jf-drawer-enter-active,
.jf-drawer-enter-active .jf-drawer-panel {
  transition:
    opacity var(--jf-duration-overlay) var(--jf-ease-out),
    transform var(--jf-duration-overlay) var(--jf-ease-out);
}

.jf-drawer-leave-active,
.jf-drawer-leave-active .jf-drawer-panel {
  transition:
    opacity var(--jf-duration-exit-overlay) var(--jf-ease-in),
    transform var(--jf-duration-exit-overlay) var(--jf-ease-in);
}

.jf-drawer-enter-from,
.jf-drawer-leave-to {
  opacity: 0;
}

.jf-drawer-enter-from .jf-drawer-panel,
.jf-drawer-leave-to .jf-drawer-panel {
  transform: translateX(100%);
}

@media (max-width: 639px) {
  .jf-drawer-panel {
    border-radius: 0;
  }
}
</style>
