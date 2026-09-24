<script setup lang="ts">
import { computed } from 'vue'
import { errorNotice } from '@/lib/errors'

/**
 * One error surface for the whole console.
 *
 * A page passes either the raw thrown value (`error`) or explicit copy (`title` /
 * `description`) when the message is a local validation result rather than a backend
 * failure. A plain string passed to `error` is treated as the title, so a ref that can
 * hold either a thrown value or a locally authored sentence needs no extra branching.
 *
 * The raw error code is deliberately not rendered: the title and description are the
 * operator-facing contract, and the request log remains the place where the machine code
 * is still visible.
 */
const props = withDefaults(defineProps<{
  error?: unknown
  title?: string
  description?: string
}>(), {
  error: undefined,
  title: '',
  description: '',
})

const notice = computed(() => {
  const resolved = typeof props.error === 'string' || props.error === undefined
    ? undefined
    : errorNotice(props.error)
  const title = props.title || (typeof props.error === 'string' ? props.error : resolved?.title) || ''
  const description = props.description || resolved?.description || ''
  return { title, description }
})
</script>

<template>
  <UAlert color="error" variant="soft" role="alert" :title="notice.title" :description="notice.description || undefined">
    <template v-if="$slots.actions" #actions>
      <slot name="actions" />
    </template>
  </UAlert>
</template>
