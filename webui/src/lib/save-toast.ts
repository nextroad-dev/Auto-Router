import { ref } from 'vue'

export const savedToastVisible = ref(false)
export const savedToastMessage = ref('更改已保存')
let timer: ReturnType<typeof setTimeout> | undefined

/** Show or refresh the single shared success toast; the default text confirms an autosave. */
export function showSavedToast(message = '更改已保存') {
  savedToastMessage.value = message
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
