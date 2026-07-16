<script setup lang="ts">
type SearchEntityType =
  | "author"
  | "benchmark"
  | "dataset"
  | "identifier"
  | "method"
  | "paper"
  | "topic"
  | "venue"

interface SearchSuggestion {
  description?: string
  entityType: SearchEntityType
  id: string
  label: string
  value?: string
}

interface SearchSuggestionGroup {
  id: string
  items: SearchSuggestion[]
  label: string
}

const props = withDefaults(
  defineProps<{
    destination?: string
    emptyQueryMessage?: string
    error?: string
    helper?: string
    id?: string
    label?: string
    loading?: boolean
    loadingMessage?: string
    modelValue?: string
    noMatchMessage?: string
    placeholder?: string
    submitLabel?: string
    suggestionGroups?: SearchSuggestionGroup[]
  }>(),
  {
    destination: "/papers",
    emptyQueryMessage: "请输入关键词",
    error: undefined,
    helper: "支持论文标题、作者、Topic、Method、Dataset、Benchmark、Venue 和标识符。",
    id: "global-search",
    label: "搜索论文与研究实体",
    loading: false,
    loadingMessage: "正在加载搜索建议",
    modelValue: undefined,
    noMatchMessage: "没有匹配结果",
    placeholder: "例如：agent、single-cell、DOI 或作者名",
    submitLabel: "搜索",
    suggestionGroups: () => [],
  },
)

const route = useRoute()
const router = useRouter()
const emit = defineEmits<{
  search: [query: string]
  select: [suggestion: SearchSuggestion]
  "update:modelValue": [value: string]
}>()

const entityTypeLabels: Record<SearchEntityType, string> = {
  author: "作者",
  benchmark: "Benchmark",
  dataset: "Dataset",
  identifier: "标识符",
  method: "Method",
  paper: "论文",
  topic: "Topic",
  venue: "Venue",
}

function routeQueryText(value: unknown) {
  if (Array.isArray(value)) {
    return typeof value[0] === "string" ? value[0] : ""
  }
  return typeof value === "string" ? value : ""
}

const internalQuery = ref(routeQueryText(route.query.q))
const query = computed({
  get: () => props.modelValue ?? internalQuery.value,
  set: (value: string) => {
    internalQuery.value = value
    emit("update:modelValue", value)
  },
})
const helperId = computed(() => `${props.id}-helper`)
const listboxId = computed(() => `${props.id}-suggestions`)
const statusId = computed(() => `${props.id}-status`)
const activeIndex = ref(-1)
const isDismissed = ref(false)

watch(
  () => route.query.q,
  (value) => {
    const restoredQuery = routeQueryText(value)
    internalQuery.value = restoredQuery
    activeIndex.value = -1
    isDismissed.value = false
    emit("update:modelValue", restoredQuery)
  },
)

const flattenedSuggestions = computed(() =>
  props.suggestionGroups.flatMap((group, groupIndex) =>
    group.items.map((item, itemIndex) => ({
      groupIndex,
      item,
      itemIndex,
      optionId: optionId(group.id, item.id, groupIndex, itemIndex),
    })),
  ),
)

const commandState = computed<
  "empty-query" | "error" | "loading" | "no-match" | "suggestions"
>(() => {
  if (props.loading) {
    return "loading"
  }
  if (props.error) {
    return "error"
  }
  if (query.value.trim().length === 0) {
    return "empty-query"
  }
  if (flattenedSuggestions.value.length === 0) {
    return "no-match"
  }
  return "suggestions"
})

const isListboxOpen = computed(
  () => commandState.value === "suggestions" && !isDismissed.value,
)

const activeOptionId = computed(() => {
  if (!isListboxOpen.value || activeIndex.value < 0) {
    return undefined
  }
  return flattenedSuggestions.value[activeIndex.value]?.optionId
})

const describedBy = computed(() =>
  commandState.value === "suggestions"
    ? helperId.value
    : `${helperId.value} ${statusId.value}`,
)

function safeId(value: string) {
  return value.replaceAll(/[^a-zA-Z0-9_-]/g, "-")
}

function groupLabelId(groupId: string, groupIndex: number) {
  return `${props.id}-group-${safeId(groupId)}-${groupIndex}-label`
}

function optionId(
  groupId: string,
  itemId: string,
  groupIndex: number,
  itemIndex: number,
) {
  return `${props.id}-option-${safeId(groupId)}-${groupIndex}-${safeId(itemId)}-${itemIndex}`
}

function updateQuery(event: Event) {
  const value = (event.target as HTMLInputElement).value
  activeIndex.value = -1
  isDismissed.value = false
  query.value = value
}

async function submitSearch() {
  const normalizedQuery = query.value.trim()
  emit("search", normalizedQuery)
  if (normalizedQuery.length === 0) {
    return
  }

  await router.push({
    path: props.destination,
    query: { q: normalizedQuery },
  })
}

function openSuggestions() {
  if (commandState.value === "suggestions") {
    isDismissed.value = false
  }
}

function moveActiveOption(direction: 1 | -1) {
  const optionCount = flattenedSuggestions.value.length
  if (optionCount === 0 || commandState.value !== "suggestions") {
    return
  }

  isDismissed.value = false
  if (activeIndex.value < 0) {
    activeIndex.value = direction === 1 ? 0 : optionCount - 1
    return
  }

  activeIndex.value =
    (activeIndex.value + direction + optionCount) % optionCount
}

function selectSuggestion(suggestion: SearchSuggestion) {
  query.value = suggestion.value ?? suggestion.label
  emit("select", suggestion)
  activeIndex.value = -1
  isDismissed.value = true
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key === "ArrowDown") {
    event.preventDefault()
    moveActiveOption(1)
    return
  }
  if (event.key === "ArrowUp") {
    event.preventDefault()
    moveActiveOption(-1)
    return
  }
  if (event.key === "Enter" && activeIndex.value >= 0 && isListboxOpen.value) {
    event.preventDefault()
    const activeSuggestion = flattenedSuggestions.value[activeIndex.value]
    if (activeSuggestion) {
      selectSuggestion(activeSuggestion.item)
    }
    return
  }
  if (event.key === "Escape") {
    event.preventDefault()
    activeIndex.value = -1
    isDismissed.value = true
  }
}
</script>

<template>
  <form
    class="search-command"
    role="search"
    :action="destination"
    method="get"
    @submit.prevent="submitSearch"
  >
    <label class="search-command__label" :for="id">
      {{ label }}
    </label>
    <div class="search-command__control">
      <input
        :id="id"
        class="search-command__input"
        name="q"
        type="search"
        role="combobox"
        :value="query"
        :placeholder="placeholder"
        aria-autocomplete="list"
        :aria-busy="loading || undefined"
        :aria-controls="listboxId"
        :aria-describedby="describedBy"
        :aria-expanded="isListboxOpen"
        :aria-activedescendant="activeOptionId"
        autocomplete="off"
        enterkeyhint="search"
        @focus="openSuggestions"
        @input="updateQuery"
        @keydown="handleKeydown"
      >
      <button class="button button--primary search-command__submit" type="submit">
        {{ submitLabel }}
      </button>
    </div>
    <p :id="helperId" class="search-command__helper">
      {{ helper }}
    </p>
    <div
      v-if="isListboxOpen"
      :id="listboxId"
      class="search-command__suggestions"
      role="listbox"
      :aria-label="`${label}建议`"
    >
      <section
        v-for="(group, groupIndex) in suggestionGroups"
        :key="group.id"
        class="search-command__group"
        role="group"
        :aria-labelledby="groupLabelId(group.id, groupIndex)"
      >
        <h2
          :id="groupLabelId(group.id, groupIndex)"
          class="search-command__group-label"
        >
          {{ group.label }}
        </h2>
        <button
          v-for="(item, itemIndex) in group.items"
          :id="optionId(group.id, item.id, groupIndex, itemIndex)"
          :key="item.id"
          class="search-command__option"
          type="button"
          role="option"
          tabindex="-1"
          :aria-selected="
            activeOptionId ===
            optionId(group.id, item.id, groupIndex, itemIndex)
          "
          :data-entity-type="item.entityType"
          @click="selectSuggestion(item)"
        >
          <span class="search-command__option-label">{{ item.label }}</span>
          <span class="search-command__option-meta">
            <span class="search-command__option-type">
              {{ entityTypeLabels[item.entityType] }}
            </span>
            <span
              v-if="item.description"
              class="search-command__option-description"
            >
              {{ item.description }}
            </span>
          </span>
        </button>
      </section>
    </div>
    <p
      v-else-if="commandState !== 'suggestions'"
      :id="statusId"
      class="search-command__state"
      :data-search-state="commandState"
      :role="commandState === 'error' ? 'alert' : 'status'"
    >
      <template v-if="commandState === 'empty-query'">
        {{ emptyQueryMessage }}
      </template>
      <template v-else-if="commandState === 'loading'">
        {{ loadingMessage }}
      </template>
      <template v-else-if="commandState === 'error'">
        {{ error }}
      </template>
      <template v-else>
        {{ noMatchMessage }}
      </template>
    </p>
  </form>
</template>

<style scoped>
.search-command {
  display: grid;
  gap: var(--space-2);
  width: 100%;
}

.search-command__label {
  color: var(--color-text-strong);
  font-size: var(--text-sm-size);
  font-weight: 700;
  line-height: var(--text-sm-line);
}

.search-command__control {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-2);
}

.search-command__input {
  min-width: 0;
  min-height: 48px;
  padding: 0 var(--space-4);
  border: 1px solid var(--color-border-control);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text);
  font: inherit;
}

.search-command__input::placeholder {
  color: var(--color-placeholder);
}

.search-command__submit {
  min-width: 72px;
}

.search-command__helper {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.search-command__suggestions {
  display: grid;
  overflow: hidden;
  border: 1px solid var(--color-border-control);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.search-command__group {
  display: grid;
}

.search-command__group + .search-command__group {
  border-top: 1px solid var(--color-border);
}

.search-command__group-label {
  margin: 0;
  padding: var(--space-2) var(--space-4);
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  line-height: var(--text-xs-line);
}

.search-command__option {
  display: grid;
  min-height: 44px;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: var(--space-3);
  align-items: center;
  padding: var(--space-2) var(--space-4);
  border: 0;
  border-top: 1px solid var(--color-border);
  background: var(--color-surface);
  color: var(--color-text);
  text-align: left;
  cursor: pointer;
}

.search-command__option[aria-selected="true"] {
  background: var(--color-surface-pressed);
  color: var(--color-text-strong);
}

.search-command__option-label {
  min-width: 0;
  overflow-wrap: anywhere;
}

.search-command__option-meta {
  display: grid;
  gap: var(--space-1);
  justify-items: end;
}

.search-command__option-type {
  color: var(--color-info);
  font-size: var(--text-xs-size);
  font-weight: 750;
  line-height: var(--text-xs-line);
}

.search-command__option-description {
  color: var(--color-text-muted);
  font-size: var(--text-xs-size);
  line-height: var(--text-xs-line);
}

.search-command__state {
  margin: 0;
  padding: var(--space-3) var(--space-4);
  border-left: 4px solid var(--color-info);
  background: var(--color-info-bg);
  color: var(--color-text);
  font-size: var(--text-sm-size);
  line-height: var(--text-sm-line);
}

.search-command__state[data-search-state="error"] {
  border-left-color: var(--color-negative);
  background: var(--color-negative-subtle);
}

@media (max-width: 374px) {
  .search-command__control {
    grid-template-columns: 1fr;
  }
}
</style>
