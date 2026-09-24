import { reactive } from 'vue'
export const sessionState = reactive({ name: '', expiresAt: '' })
export function clearSession() { sessionState.name = ''; sessionState.expiresAt = '' }
