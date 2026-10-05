import { h } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import JfField from '../src/components/JfField.vue'
import JfInput from '../src/components/JfInput.vue'

describe('JfField operator help', () => {
  it('associates both help and validation with the control, updating independently', async () => {
    const wrapper = mount(JfField, {
      props: { name: 'jev-mode', label: '发送给 Jev 的内容', help: '本地截取，不发送完整对话。', error: '请选择受支持的模式。' },
      slots: { default: () => h(JfInput) },
    })
    const control = wrapper.get('input')
    expect(control.attributes('aria-describedby')).toBe('jev-mode-help jev-mode-message')
    expect(control.attributes('aria-invalid')).toBe('true')
    expect(wrapper.get('#jev-mode-help').text()).toContain('不发送完整对话')

    await wrapper.setProps({ error: undefined })
    expect(control.attributes('aria-describedby')).toBe('jev-mode-help')
    expect(control.attributes('aria-invalid')).toBeUndefined()
    expect(wrapper.find('#jev-mode-message').exists()).toBe(false)

    await wrapper.setProps({ help: undefined })
    expect(control.attributes('aria-describedby')).toBeUndefined()
    wrapper.unmount()
  })
})
