import { nextTick, onScopeDispose, watch, type Ref } from 'vue'

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

let locks = 0
let savedBodyOverflow = ''

// Open surfaces in opening order. Only the topmost one answers Escape and contains Tab, so a
// confirmation raised from inside a drawer neither closes the drawer nor loses focus to it.
const openStack: symbol[] = []

function lockPageScroll() {
  if (locks === 0) {
    savedBodyOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  }
  locks += 1
}

function releasePageScroll() {
  locks = Math.max(0, locks - 1)
  if (locks === 0) document.body.style.overflow = savedBodyOverflow
}

/**
 * Focus containment for a modal surface: move focus in on open, keep Tab inside the
 * panel, hand it back to the trigger on close, and stop the page behind from scrolling.
 *
 * `dismissible` lets a surface refuse Escape (a one-time secret must be acknowledged).
 */
export function useOverlayFocus(options: {
  open: Ref<boolean>
  panel: Ref<HTMLElement | null | undefined>
  dismissible?: Ref<boolean | undefined>
  onEscape?: () => void
}) {
  let returnFocusTo: HTMLElement | null = null
  const token = Symbol('overlay')

  function leaveStack() {
    const index = openStack.indexOf(token)
    if (index >= 0) openStack.splice(index, 1)
  }

  function onKeyDown(event: KeyboardEvent) {
    if (!options.open.value || openStack.at(-1) !== token) return

    if (event.key === 'Escape') {
      if (options.dismissible && options.dismissible.value === false) return
      event.preventDefault()
      event.stopPropagation()
      options.onEscape?.()
      return
    }

    if (event.key !== 'Tab' || !options.panel) return
    const panel = options.panel.value
    if (!panel) return
    const nodes = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE))
      .filter(node => node.offsetWidth > 0 || node.offsetHeight > 0 || node === document.activeElement)
    if (!nodes.length) {
      event.preventDefault()
      return
    }

    const first = nodes[0]!
    const last = nodes[nodes.length - 1]!
    const active = document.activeElement as HTMLElement | null

    if (event.shiftKey && (active === first || !panel.contains(active))) {
      event.preventDefault()
      last.focus()
    }
    else if (!event.shiftKey && (active === last || !panel.contains(active))) {
      event.preventDefault()
      first.focus()
    }
  }

  watch(options.open, async (value) => {
    if (value) {
      returnFocusTo = document.activeElement as HTMLElement | null
      openStack.push(token)
      lockPageScroll()
      document.addEventListener('keydown', onKeyDown, true)
      await nextTick()
      const panel = options.panel.value
      const target = panel?.querySelector<HTMLElement>('[data-autofocus]') ?? panel?.querySelector<HTMLElement>(FOCUSABLE)
      target?.focus()
      return
    }

    document.removeEventListener('keydown', onKeyDown, true)
    leaveStack()
    releasePageScroll()
    returnFocusTo?.focus?.()
    returnFocusTo = null
  })

  onScopeDispose(() => {
    if (options.open.value) {
      document.removeEventListener('keydown', onKeyDown, true)
      leaveStack()
      releasePageScroll()
    }
  })
}
