<script setup lang="ts">
const props = withDefaults(
  defineProps<{
    action?: string
    helper?: string
    id?: string
    label?: string
    modelValue?: string
    placeholder?: string
    submitLabel?: string
  }>(),
  {
    action: "/papers",
    helper: "支持论文标题、作者、Topic、Method、Dataset、Benchmark、Venue 和标识符。",
    id: "global-search",
    label: "搜索论文与研究实体",
    modelValue: "",
    placeholder: "例如：agent、single-cell、DOI 或作者名",
    submitLabel: "搜索",
  },
)

const emit = defineEmits<{
  search: [query: string]
  "update:modelValue": [value: string]
}>()

const query = ref(props.modelValue)
const helperId = computed(() => `${props.id}-helper`)

watch(
  () => props.modelValue,
  (value) => {
    query.value = value
  },
)

function updateQuery(event: Event) {
  const value = (event.target as HTMLInputElement).value
  query.value = value
  emit("update:modelValue", value)
}

function submitSearch() {
  emit("search", query.value)
}
</script>

<template>
  <form
    class="search-command"
    role="search"
    :action="action"
    method="get"
    @submit="submitSearch"
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
        :value="query"
        :placeholder="placeholder"
        :aria-describedby="helperId"
        autocomplete="off"
        enterkeyhint="search"
        @input="updateQuery"
      >
      <button class="button button--primary search-command__submit" type="submit">
        {{ submitLabel }}
      </button>
    </div>
    <p :id="helperId" class="search-command__helper">
      {{ helper }}
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
  color: var(--warm-500);
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

@media (max-width: 374px) {
  .search-command__control {
    grid-template-columns: 1fr;
  }
}
</style>
