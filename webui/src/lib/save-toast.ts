import { ref } from 'vue'

export const savedToastVisible = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined

/** Show or refresh the single shared success toast. */
export function showSavedToast() {
  savedToastVisible.value = true
  if (timer !== undefined) clearTimeout(timer)
  timer = setTimeout(() => {
    savedToastVisible.value = false
    timer = undefined
  }, 3000)
}

export function dismissSavedToast() {
  if (timer !== undefined) clearTimeout(timer)
  timer = undefined
  savedToastVisible.value = false
}
