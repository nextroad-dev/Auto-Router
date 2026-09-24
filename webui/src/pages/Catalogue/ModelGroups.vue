<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, errorMessage, getAllPages, type Pair } from '@/lib/api'
import type { GroupModel, ModelGroupsDocument } from '@/lib/admin-contracts'
import { router } from '@/router'

type GroupName = 'simple' | 'medium' | 'complex'
const groupDefinitions: Array<{ key: GroupName; label: string }> = [
  { key: 'simple', label: '简单任务' },
  { key: 'medium', label: '中等任务' },
  { key: 'complex', label: '复杂任务' },
]

const groups = ref<Record<GroupName, GroupModel[]>>({ simple: [], medium: [], complex: [] })
const availablePairs = ref<Pair[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const success = ref('')

function pairKey(pair: GroupModel) {
  return `${pair.provider}\u0000${pair.model}`
}

async function loadGroups() {
  loading.value = true
  error.value = ''
  success.value = ''
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
    error.value = errorMessage(cause)
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
}

function movePair(group: GroupName, index: number, direction: -1 | 1) {
  const current = [...groups.value[group]]
  const next = index + direction
  if (next < 0 || next >= current.length) return
  ;[current[index], current[next]] = [current[next]!, current[index]!]
  groups.value[group] = current
}

async function saveGroups() {
  saving.value = true
  error.value = ''
  success.value = ''
  try {
    await api.put('/admin/v1/groups', {
      simple: groups.value.simple,
      medium: groups.value.medium,
      complex: groups.value.complex,
    })
    success.value = '分组和选择顺序已保存。'
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    saving.value = false
  }
}

onMounted(() => { void loadGroups() })
</script>

<template>
  <div class="space-y-6">
    <section class="flex flex-col justify-between gap-4 sm:flex-row sm:items-end">
      <div>
        <p class="mt-1 text-sm text-muted">为不同复杂度的任务选择候选路由，并调整组内顺序。</p>
      </div>
      <div class="flex gap-2">
        <UButton color="neutral" variant="outline" icon="i-heroicons-arrow-path" :loading="loading" @click="loadGroups">刷新</UButton>
        <UButton icon="i-heroicons-check" :loading="saving" @click="saveGroups">保存分组</UButton>
      </div>
    </section>

    <UAlert v-if="error" color="error" variant="soft" :title="error" />
    <UAlert v-if="success" color="success" variant="soft" :title="success" />
    <UCard v-if="!availablePairs.length && !loading" class="border-dashed">
      <div class="py-5 text-center">
        <h3 class="font-medium">还没有可选路由</h3>
        <p class="mt-1 text-sm text-muted">先在提供商页面添加模型。</p>
        <UButton class="mt-4" variant="soft" @click="router.push('/providers')">前往提供商</UButton>
      </div>
    </UCard>

    <section class="grid gap-4 xl:grid-cols-3">
      <UCard v-for="definition in groupDefinitions" :key="definition.key" class="min-w-0">
        <template #header>
          <div class="flex items-center justify-between gap-3">
            <h3 class="font-semibold">{{ definition.label }}</h3>
            <UBadge color="neutral" variant="subtle">{{ groups[definition.key].length }} 个</UBadge>
          </div>
        </template>
        <div v-if="loading" class="space-y-3"><USkeleton v-for="n in 4" :key="n" class="h-9 w-full" /></div>
        <div v-else class="space-y-4">
          <div class="max-h-72 space-y-2 overflow-y-auto">
            <UCheckbox
              v-for="pair in availablePairs"
              :key="pairKey(pair)"
              :model-value="isSelected(definition.key, pair)"
              :label="`${pair.provider} · ${pair.model}`"
              @update:model-value="togglePair(definition.key, pair, $event)"
            />
          </div>
          <USeparator />
          <div>
            <h4 class="mb-2 text-sm font-medium">选择顺序</h4>
            <ol v-if="groups[definition.key].length" class="space-y-2">
              <li v-for="(pair, index) in groups[definition.key]" :key="pairKey(pair)" class="flex items-center gap-2 rounded-lg border border-default px-3 py-2">
                <span class="w-5 shrink-0 text-xs tabular-nums text-muted">{{ index + 1 }}</span>
                <span class="min-w-0 flex-1 truncate text-xs"><span class="text-muted">{{ pair.provider }} · </span><span class="font-mono">{{ pair.model }}</span></span>
                <UButton color="neutral" variant="ghost" size="xs" icon="i-heroicons-chevron-up" :aria-label="`上移 ${pair.provider} ${pair.model}`" :disabled="index === 0" @click="movePair(definition.key, index, -1)" />
                <UButton color="neutral" variant="ghost" size="xs" icon="i-heroicons-chevron-down" :aria-label="`下移 ${pair.provider} ${pair.model}`" :disabled="index === groups[definition.key].length - 1" @click="movePair(definition.key, index, 1)" />
              </li>
            </ol>
            <p v-else class="rounded-lg bg-elevated/50 px-3 py-4 text-center text-sm text-muted">选择路由以设置优先顺序。</p>
          </div>
        </div>
      </UCard>
    </section>
  </div>
</template>
