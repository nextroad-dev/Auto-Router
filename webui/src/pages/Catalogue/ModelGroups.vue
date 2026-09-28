<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, getAllPages, type Pair } from '@/lib/api'
import { createSerialAutosaveQueue } from '@/lib/autosave'
import { showSavedToast } from '@/lib/save-toast'
import { errorNotice } from '@/lib/errors'
import ErrorAlert from '@/components/ErrorAlert.vue'
import JfBadge from '@/components/JfBadge.vue'
import JfButton from '@/components/JfButton.vue'
import JfCard from '@/components/JfCard.vue'
import JfCheckbox from '@/components/JfCheckbox.vue'
import JfEmpty from '@/components/JfEmpty.vue'
import JfSkeleton from '@/components/JfSkeleton.vue'
import type { GroupModel, ModelGroupsDocument } from '@/lib/admin-contracts'
import { router } from '@/router'

type GroupName = 'simple' | 'medium' | 'complex'

const groupDefinitions: Array<{ key: GroupName; label: string }> = [
  { key: 'simple', label: '简单任务组' },
  { key: 'medium', label: '中等任务组' },
  { key: 'complex', label: '复杂任务组' },
]

const groups = ref<Record<GroupName, GroupModel[]>>({ simple: [], medium: [], complex: [] })
const availablePairs = ref<Pair[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref<unknown>()

function pairKey(pair: GroupModel) {
  return `${pair.provider}\u0000${pair.model}`
}

type GroupSnapshot = Record<GroupName, GroupModel[]>
const saveQueue = createSerialAutosaveQueue<GroupSnapshot>(async snapshot => {
  saving.value = true
  try {
    await api.put('/admin/v1/groups', snapshot)
    error.value = undefined
    showSavedToast()
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    saving.value = false
  }
}, (_current, next) => next)

function persistGroups() {
  saveQueue.enqueue({
    simple: groups.value.simple.map(item => ({ ...item })),
    medium: groups.value.medium.map(item => ({ ...item })),
    complex: groups.value.complex.map(item => ({ ...item })),
  })
}

async function loadGroups() {
  loading.value = true
  error.value = undefined
  try {
    const [document, pairs] = await Promise.all([
      api.get<ModelGroupsDocument>('/admin/v1/groups'),
      getAllPages<Pair>('/admin/v1/pairs'),
    ])
    groups.value = {
      simple: [...(document.simple ?? [])],
      medium: [...(document.medium ?? [])],
      complex: [...(document.complex ?? [])],
    }
    availablePairs.value = pairs.sort((a, b) => a.provider.localeCompare(b.provider) || a.model.localeCompare(b.model))
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

function isSelected(group: GroupName, pair: Pair) {
  return groups.value[group].some(item => item.provider === pair.provider && item.model === pair.model)
}

function togglePair(group: GroupName, pair: Pair, checked: boolean) {
  const current = [...groups.value[group]]
  const key = pairKey(pair)
  const existing = current.findIndex(item => pairKey(item) === key)
  if (checked && existing < 0) current.push({ provider: pair.provider, model: pair.model })
  if (!checked && existing >= 0) current.splice(existing, 1)
  groups.value[group] = current
  persistGroups()
}

function movePair(group: GroupName, index: number, direction: -1 | 1) {
  const current = [...groups.value[group]]
  const next = index + direction
  if (next < 0 || next >= current.length) return
  ;[current[index], current[next]] = [current[next]!, current[index]!]
  groups.value[group] = current
  persistGroups()
}

onMounted(() => { void loadGroups() })
</script>

<template>
  <div class="jf-stack">
    <!-- Toolbar -->
    <section class="jf-toolbar">
      <div>
        <h1 class="jf-page-title">模型分组</h1>
      </div>
      <div class="jf-action-group">
        <JfButton variant="secondary" icon="arrow-path" :loading="loading || saving" @click="loadGroups">刷新</JfButton>
      </div>
    </section>

    <ErrorAlert v-if="error" :error="error" />

    <JfEmpty
      v-if="!availablePairs.length && !loading"
      variant="first-use"
      title="尚无可用模型路由绑定"
    >
      <template #action>
        <JfButton variant="secondary" @click="router.push('/providers')">前往配置提供商</JfButton>
      </template>
    </JfEmpty>

    <!-- Group Cards Grid -->
    <section class="grid gap-6 xl:grid-cols-3">
      <JfCard
        v-for="definition in groupDefinitions"
        :key="definition.key"
        density="compact"
        :title="definition.label"
      >
        <template #actions>
          <JfBadge tone="neutral">{{ groups[definition.key].length }} 个候选</JfBadge>
        </template>

        <div v-if="loading" class="grid gap-2" aria-busy="true">
          <JfSkeleton v-for="n in 4" :key="n" height="36px" shape="block" />
        </div>

        <div v-else class="grid gap-4">
          <!-- Candidate Picker -->
          <div>
            <h4 class="jf-module-title mb-2">选择候选模型</h4>
            <div class="pair-picker max-h-64 overflow-y-auto rounded-[var(--jf-radius-control)] bg-tonal p-2.5">
              <div v-if="availablePairs.length" class="space-y-0.5">
                <JfCheckbox
                  v-for="pair in availablePairs"
                  :key="pairKey(pair)"
                  :model-value="isSelected(definition.key, pair)"
                  :label="`${pair.provider} · ${pair.model}`"
                  class="rounded-[var(--jf-radius-control)] px-2 py-1 transition-colors hover:bg-tonal-hover"
                  @update:model-value="togglePair(definition.key, pair, $event)"
                />
              </div>
              <p v-else class="jf-caption text-ink-secondary py-2 text-center">暂无可用模型</p>
            </div>
          </div>

          <!-- Priority Ordering -->
          <div>
            <h4 class="jf-module-title mb-2">组内候选优先次序</h4>
            <ol v-if="groups[definition.key].length" class="grid gap-1.5">
              <li
                v-for="(pair, index) in groups[definition.key]"
                :key="pairKey(pair)"
                class="flex items-center gap-2 rounded-[var(--jf-radius-control)] bg-tonal px-3 py-2 transition-colors hover:bg-tonal-hover"
              >
                <span class="jf-caption jf-tabular jf-nowrap w-5 shrink-0 text-ink-secondary font-medium">
                  {{ index + 1 }}
                </span>
                <span class="jf-caption jf-truncate flex-1">
                  <span class="text-ink-secondary">{{ pair.provider }} · </span>
                  <span class="font-mono font-medium">{{ pair.model }}</span>
                </span>
                <div class="flex items-center gap-1 shrink-0">
                  <JfButton
                    variant="ghost"
                    square
                    size="sm"
                    icon="chevron-up"
                    :aria-label="`上移 ${pair.provider} ${pair.model}`"
                    :disabled="index === 0"
                    @click="movePair(definition.key, index, -1)"
                  />
                  <JfButton
                    variant="ghost"
                    square
                    size="sm"
                    icon="chevron-down"
                    :aria-label="`下移 ${pair.provider} ${pair.model}`"
                    :disabled="index === groups[definition.key].length - 1"
                    @click="movePair(definition.key, index, 1)"
                  />
                </div>
              </li>
            </ol>
            <p v-else class="py-2 text-center text-sm text-ink-secondary">暂无候选模型</p>
          </div>
        </div>
      </JfCard>
    </section>
  </div>
</template>

<style scoped>
.pair-picker :deep(.jf-checkbox-text) {
  overflow-wrap: anywhere;
}
</style>
