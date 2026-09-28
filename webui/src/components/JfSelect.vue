<script setup lang="ts" generic="T = unknown">
import { computed, inject, nextTick, ref, useAttrs, useId, useTemplateRef } from 'vue'
import { useEventListener } from '@vueuse/core'
import JfIcon from './JfIcon.vue'
import { jfFieldKey } from '@/lib/field-context'
import { splitControlAttrs } from '@/lib/control-attrs'

defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{
  modelValue?: T
  items?: Array<{ label: string; value: T }>
  /** Accessible name; required when the select is not wrapped in a JfField. */
  label?: string
  placeholder?: string
  disabled?: boolean
  size?: 'sm' | 'md' | 'lg'
  invalid?: boolean
}>(), {
  modelValue: undefined,
  items: () => [],
  label: undefined,
  placeholder: undefined,
  disabled: false,
  size: 'md',
  invalid: false,
})

const emit = defineEmits<{ 'update:modelValue': [value: T] }>()

const attrs = useAttrs()
const split = computed(() => splitControlAttrs(attrs as Record<string, unknown>))
const field = inject(jfFieldKey)

const open = ref(false)
const activeIndex = ref(-1)
const trigger = useTemplateRef<HTMLButtonElement>('trigger')
const menu = useTemplateRef<HTMLElement>('menu')
const menuStyle = ref<Record<string, string>>({})

const uid = useId()
const listboxId = `jf-select-listbox-${uid}`
const optionId = (index: number) => `${listboxId}-${index}`

const selectedIndex = computed(() => props.items.findIndex(item => item.value === props.modelValue))
const displayLabel = computed(() => (selectedIndex.value >= 0 ? props.items[selectedIndex.value]!.label : ''))
const invalid = computed(() => props.invalid || Boolean(field?.invalid))

function openMenu() {
  if (props.disabled || !props.items.length) return
  open.value = true
  activeIndex.value = selectedIndex.value >= 0 ? selectedIndex.value : 0
  void nextTick(place)
}

function closeMenu(returnFocus = true) {
  if (!open.value) return
  open.value = false
  activeIndex.value = -1
  if (returnFocus) trigger.value?.focus()
}

/** The menu is teleported to the body so a scrolling drawer or dialog cannot clip it. */
function place() {
  const anchor = trigger.value
  const panel = menu.value
  if (!anchor || !panel) return
  const rect = anchor.getBoundingClientRect()
  const gap = 4
  const spaceBelow = window.innerHeight - rect.bottom - gap
  const spaceAbove = rect.top - gap
  const wanted = Math.min(panel.scrollHeight, 304)
  const flip = spaceBelow < Math.min(wanted, 144) && spaceAbove > spaceBelow
  const room = Math.max(120, (flip ? spaceAbove : spaceBelow) - 8)

  menuStyle.value = {
    left: `${rect.left}px`,
    width: `${Math.max(rect.width, 168)}px`,
    maxHeight: `${room}px`,
    transformOrigin: flip ? 'bottom left' : 'top left',
    ...(flip ? { bottom: `${window.innerHeight - rect.top + gap}px` } : { top: `${rect.bottom + gap}px` }),
  }
}

function scrollActiveIntoView() {
  const node = activeIndex.value >= 0 ? document.getElementById(optionId(activeIndex.value)) : null
  node?.scrollIntoView({ block: 'nearest' })
}

function moveActive(step: number) {
  if (!open.value) {
    openMenu()
    return
  }
  const last = props.items.length - 1
  activeIndex.value = Math.min(last, Math.max(0, (activeIndex.value < 0 ? 0 : activeIndex.value) + step))
  void nextTick(scrollActiveIntoView)
}

function choose(index: number) {
  const item = props.items[index]
  if (!item) return
  emit('update:modelValue', item.value)
  closeMenu()
}

function onTriggerKeydown(event: KeyboardEvent) {
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault()
      moveActive(1)
      break
    case 'ArrowUp':
      event.preventDefault()
      moveActive(-1)
      break
    case 'Home':
      event.preventDefault()
      if (open.value) {
        activeIndex.value = 0
        void nextTick(scrollActiveIntoView)
      }
      break
    case 'End':
      event.preventDefault()
      if (open.value) {
        activeIndex.value = props.items.length - 1
        void nextTick(scrollActiveIntoView)
      }
      break
    case 'Enter':
    case ' ':
      event.preventDefault()
      if (open.value) choose(activeIndex.value)
      else openMenu()
      break
    case 'Escape':
      if (open.value) {
        event.preventDefault()
        closeMenu()
      }
      break
    case 'Tab':
      closeMenu(false)
      break
  }
}

// The menu is teleported to the body, so "outside" has to account for the trigger
// and the panel separately.
function onDocPointerDown(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as Node | null
  if (!target || trigger.value?.contains(target) || menu.value?.contains(target)) return
  closeMenu(false)
}

useEventListener(document, 'pointerdown', onDocPointerDown, true)

// A scroll elsewhere keeps the menu attached to its trigger; a resize has no
// reliable geometry, so it closes rather than floating away.
useEventListener(window, 'scroll', () => {
  if (open.value) place()
}, { capture: true })

useEventListener(window, 'resize', () => {
  if (open.value) closeMenu(false)
})

</script>

<template>
  <div class="jf-select" :class="split.wrapper.class" :data-size="size" :data-invalid="invalid || undefined">
    <button
      ref="trigger"
      type="button"
      role="combobox"
      class="jf-select-trigger"
      :id="field?.controlId"
      :disabled="disabled"
      :aria-expanded="open ? 'true' : 'false'"
      :aria-controls="open ? listboxId : undefined"
      :aria-activedescendant="open && activeIndex >= 0 ? optionId(activeIndex) : undefined"
      aria-haspopup="listbox"
      :aria-label="label"
      :aria-invalid="invalid ? 'true' : undefined"
      :aria-describedby="field?.describedBy"
      v-bind="split.control"
      @click="open ? closeMenu() : openMenu()"
      @keydown="onTriggerKeydown"
    >
      <span class="jf-select-value" :data-empty="displayLabel ? undefined : 'true'">{{ displayLabel || placeholder || '请选择' }}</span>
      <JfIcon name="chevron-up-down" size="sm" class="jf-select-chevron" aria-hidden="true" />
    </button>

    <Teleport to="body">
      <div
        v-if="open"
        ref="menu"
        :id="listboxId"
        class="jf-select-menu"
        role="listbox"
        :aria-label="label"
        :style="menuStyle"
      >
        <div
          v-for="(item, index) in items"
          :key="`${String(item.value)}-${index}`"
          :id="optionId(index)"
          class="jf-select-option"
          role="option"
          :data-active="index === activeIndex || undefined"
          :aria-selected="index === selectedIndex ? 'true' : 'false'"
          @click="choose(index)"
          @pointerenter="activeIndex = index"
        >
          <span class="jf-select-option-label">{{ item.label }}</span>
          <JfIcon v-if="index === selectedIndex" name="check" size="sm" class="jf-select-check" aria-hidden="true" />
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.jf-select {
  position: relative;
  min-width: 0;
}

.jf-select-trigger {
  display: flex;
  width: 100%;
  min-width: 0;
  min-height: var(--jf-control-md);
  align-items: center;
  justify-content: space-between;
  gap: var(--jf-space-2);
  border: 1px solid var(--jf-border-control);
  border-radius: var(--jf-radius-control);
  background: var(--jf-surface);
  padding: 0 var(--jf-space-3);
  color: var(--jf-text);
  text-align: left;
  cursor: pointer;
  transition:
    border-color var(--jf-duration-fast) var(--jf-ease),
    background var(--jf-duration-fast) var(--jf-ease);
}

.jf-select[data-size='sm'] .jf-select-trigger {
  min-height: var(--jf-control-sm);
  font-size: var(--jf-caption-size);
}

.jf-select[data-size='lg'] .jf-select-trigger {
  min-height: var(--jf-control-lg);
  font-size: var(--jf-body-size);
}

.jf-select-trigger:hover:not(:disabled) {
  border-color: var(--jf-border-strong);
}

.jf-select[data-invalid] .jf-select-trigger {
  border-color: var(--jf-danger);
}

.jf-select-trigger:disabled {
  background: var(--jf-tonal);
  opacity: var(--jf-opacity-disabled);
  cursor: not-allowed;
}

.jf-select-value {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.jf-select-value[data-empty] {
  color: var(--jf-text-secondary);
}

.jf-select-chevron {
  flex: none;
  color: var(--jf-text-secondary);
}

.jf-select-menu {
  position: fixed;
  z-index: var(--jf-z-tooltip);
  overflow-y: auto;
  border: 1px solid var(--jf-border-subtle);
  border-radius: var(--jf-radius-popup);
  background: var(--jf-surface);
  box-shadow: var(--jf-shadow-dropdown);
  padding: 6px;
  animation: jf-fade-in-scale var(--jf-duration-normal) var(--jf-ease-out) both;
}

.jf-select-option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--jf-space-2);
  min-height: 36px;
  border-radius: 8px;
  padding: 0 var(--jf-space-3);
  cursor: pointer;
  transition: background var(--jf-duration-fast) var(--jf-ease);
}

.jf-select-option[data-active] {
  background: var(--jf-tonal);
}

.jf-select-option:active {
  background: var(--jf-tonal-active);
}

.jf-select-option[aria-selected='true'] {
  font-weight: var(--jf-emphasis-weight);
}

.jf-select-option-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.jf-select-check {
  flex: none;
  color: var(--jf-text);
}
</style>
