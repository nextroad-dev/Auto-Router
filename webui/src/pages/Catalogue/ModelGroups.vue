<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, getAllPages, type Model, type Pair, type Provider } from '@/lib/api'
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

// The server rejects a group larger than this; see storage.ReplaceModelGroups.
const MAX_GROUP_MEMBERS = 8

const groupDefinitions: Array<{ key: GroupName; label: string }> = [
  { key: 'simple', label: '简单任务组' },
  { key: 'medium', label: '中等任务组' },
  { key: 'complex', label: '复杂任务组' },
]

const groups = ref<Record<GroupName, GroupModel[]>>({ simple: [], medium: [], complex: [] })
const availablePairs = ref<Pair[]>([])
// Why a pair cannot be a group member, keyed by pairKey. A missing entry means routable.
const unavailableReasons = ref<Record<string, string>>({})
const loading = ref(false)
const saving = ref(false)
const error = ref<unknown>()

function pairKey(pair: GroupModel) {
  return `${pair.provider}\u0000${pair.model}`
}

type GroupSnapshot = Record<GroupName, GroupModel[]>
type GroupSave = { id: number; snapshot: GroupSnapshot }

function cloneGroups(source: GroupSnapshot): GroupSnapshot {
  return {
    simple: source.simple.map(item => ({ ...item })),
    medium: source.medium.map(item => ({ ...item })),
    complex: source.complex.map(item => ({ ...item })),
  }
}

// The last snapshot the server accepted. A rejected save rolls the page back to it so
// the checkboxes never show a membership that was not stored.
let confirmedGroups: GroupSnapshot = { simple: [], medium: [], complex: [] }
let latestSaveId = 0

const saveQueue = createSerialAutosaveQueue<GroupSave>(async ({ id, snapshot }) => {
  saving.value = true
  try {
    await api.put('/admin/v1/groups', snapshot)
    confirmedGroups = cloneGroups(snapshot)
    error.value = undefined
    showSavedToast()
  } catch (cause) {
    error.value = errorNotice(cause)
    // A newer edit is still queued; let it decide what the page shows.
    if (id === latestSaveId) groups.value = cloneGroups(confirmedGroups)
  } finally {
    saving.value = false
  }
}, (_current, next) => next)

function persistGroups() {
  saveQueue.enqueue({ id: ++latestSaveId, snapshot: cloneGroups(groups.value) })
}

function pairUnavailableReasons(pairs: Pair[], providers: Provider[], models: Model[]) {
  const providerEnabled = new Map(providers.map(item => [item.key, item.enabled]))
  const modelEnabled = new Map(models.map(item => [item.id, item.enabled]))
  const reasons: Record<string, string> = {}
  for (const pair of pairs) {
    const key = pairKey(pair)
    if (!pair.enabled) reasons[key] = '绑定已停用'
    else if (providerEnabled.get(pair.provider) !== true) reasons[key] = '提供商已停用'
    else if (modelEnabled.get(pair.model) !== true) reasons[key] = '模型已停用'
  }
  return reasons
}

async function loadGroups() {
  loading.value = true
  error.value = undefined
  try {
    const [document, pairs, providers, models] = await Promise.all([
      api.get<ModelGroupsDocument>('/admin/v1/groups'),
      getAllPages<Pair>('/admin/v1/pairs'),
      getAllPages<Provider>('/admin/v1/providers'),
      getAllPages<Model>('/admin/v1/models'),
    ])
    groups.value = {
      simple: [...(document.simple ?? [])],
      medium: [...(document.medium ?? [])],
      complex: [...(document.complex ?? [])],
    }
    confirmedGroups = cloneGroups(groups.value)
    availablePairs.value = pairs.sort((a, b) => a.provider.localeCompare(b.provider) || a.model.localeCompare(b.model))
    unavailableReasons.value = pairUnavailableReasons(pairs, providers, models)
  } catch (cause) {
    error.value = errorNotice(cause)
  } finally {
    loading.value = false
  }
}

/** Why a group member cannot be routed to, or an empty string when it can. */
function memberProblem(member: GroupModel) {
  const key = pairKey(member)
  if (!availablePairs.value.some(pair => pairKey(pair) === key)) return '绑定不存在'
  return unavailableReasons.value[key] ?? ''
}

function isSelected(group: GroupName, pair: Pair) {
  return groups.value[group].some(item => item.provider === pair.provider && item.model === pair.model)
}

function isGroupFull(group: GroupName) {
  return groups.value[group].length >= MAX_GROUP_MEMBERS
}

/** A pair can be checked only when it is routable and the group has room; unchecking is always allowed. */
function pickerDisabled(group: GroupName, pair: Pair) {
  if (isSelected(group, pair)) return false
  return Boolean(unavailableReasons.value[pairKey(pair)]) || isGroupFull(group)
}

function pickerLabel(pair: Pair) {
  const reason = unavailableReasons.value[pairKey(pair)]
  return reason ? `${pair.provider} · ${pair.model}（${reason}）` : `${pair.provider} · ${pair.model}`
}

function togglePair(group: GroupName, pair: Pair, checked: boolean) {
  const current = [...groups.value[group]]
  const key = pairKey(pair)
  const existing = current.findIndex(item => pairKey(item) === key)
  if (checked && existing < 0) {
    if (pickerDisabled(group, pair)) return
    current.push({ provider: pair.provider, model: pair.model })
  }
  if (!checked && existing >= 0) current.splice(existing, 1)
  groups.value[group] = current
  persistGroups()
}

function removeMember(group: GroupName, index: number) {
  const current = [...groups.value[group]]
  current.splice(index, 1)
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
          <JfBadge :tone="isGroupFull(definition.key) ? 'warning' : 'neutral'">{{ groups[definition.key].length }} / {{ MAX_GROUP_MEMBERS }} 个候选</JfBadge>
        </template>

        <div v-if="loading" class="grid gap-2" aria-busy="true">
          <JfSkeleton v-for="n in 4" :key="n" height="36px" shape="block" />
        </div>

        <div v-else class="grid min-w-0 gap-4">
          <!-- Candidate Picker -->
          <div>
            <h4 class="jf-module-title mb-2">选择候选模型</h4>
            <div class="pair-picker min-w-0 max-h-64 overflow-y-auto rounded-[var(--jf-radius-control)] bg-tonal p-2.5">
              <div v-if="availablePairs.length" class="space-y-0.5">
                <JfCheckbox
                  v-for="pair in availablePairs"
                  :key="pairKey(pair)"
                  :model-value="isSelected(definition.key, pair)"
                  :label="pickerLabel(pair)"
                  :disabled="pickerDisabled(definition.key, pair)"
                  class="rounded-[var(--jf-radius-control)] px-2 py-1 transition-colors hover:bg-tonal-hover"
                  @update:model-value="togglePair(definition.key, pair, $event)"
                />
              </div>
              <p v-else class="jf-caption text-ink-secondary py-2 text-center">暂无可用模型</p>
            </div>
            <p v-if="isGroupFull(definition.key)" class="jf-caption mt-1.5 text-ink-secondary">每组最多 {{ MAX_GROUP_MEMBERS }} 个候选，移除后才能继续添加。</p>
          </div>

          <!-- Priority Ordering -->
          <div>
            <h4 class="jf-module-title mb-2">组内候选优先次序</h4>
            <p v-if="groups[definition.key].some(memberProblem)" class="jf-caption mb-2 text-warning">
              组内有不可用的成员，服务端会拒绝保存整个分组；请先移除标记的成员。
            </p>
            <ol v-if="groups[definition.key].length" class="grid min-w-0 gap-1.5">
              <li
                v-for="(pair, index) in groups[definition.key]"
                :key="pairKey(pair)"
                class="flex min-w-0 flex-wrap items-center gap-2 rounded-[var(--jf-radius-control)] bg-tonal px-3 py-2 transition-colors hover:bg-tonal-hover"
              >
                <span class="jf-caption jf-tabular jf-nowrap w-5 shrink-0 text-ink-secondary font-medium">
                  {{ index + 1 }}
                </span>
                <span class="jf-caption jf-truncate min-w-20 flex-1" :title="`${pair.provider} · ${pair.model}`">
                  <span class="text-ink-secondary">{{ pair.provider }} · </span>
                  <span class="font-mono font-medium">{{ pair.model }}</span>
                </span>
                <JfBadge v-if="memberProblem(pair)" tone="warning" class="jf-nowrap shrink-0">{{ memberProblem(pair) }}</JfBadge>
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
                  <JfButton
                    variant="danger-ghost"
                    square
                    size="sm"
                    icon="x-mark"
                    :aria-label="`移除 ${pair.provider} ${pair.model}`"
                    @click="removeMember(definition.key, index)"
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
