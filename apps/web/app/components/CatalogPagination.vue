<script setup lang="ts">
import type { Pagination } from "~/types/catalog"

const props = defineProps<{
  pagination: Pagination
}>()

const route = useRoute()
const firstPageTo = computed(() => {
  const query = { ...route.query }
  delete query.cursor
  return { path: route.path, query }
})
const nextPageTo = computed(() => {
  if (!props.pagination.next_cursor) {
    return undefined
  }
  return {
    path: route.path,
    query: {
      ...route.query,
      cursor: props.pagination.next_cursor,
    },
  }
})
</script>

<template>
  <nav class="catalog-pagination" aria-label="目录分页">
    <p>
      <template v-if="pagination.total !== undefined">
        共 {{ pagination.total }} 条；
      </template>
      本页最多 {{ pagination.limit }} 条
    </p>
    <div>
      <NuxtLink
        v-if="route.query.cursor"
        class="button button--secondary"
        :to="firstPageTo"
      >
        返回第一页
      </NuxtLink>
      <NuxtLink
        v-if="pagination.has_more && nextPageTo"
        class="button button--primary"
        :to="nextPageTo"
      >
        下一页
      </NuxtLink>
    </div>
  </nav>
</template>

<style scoped>
.catalog-pagination {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-4);
  align-items: center;
  justify-content: space-between;
  padding-top: var(--space-4);
  border-top: 1px solid var(--color-border);
}

p {
  margin: 0;
  color: var(--color-text-muted);
  font-size: var(--text-sm-size);
}

.catalog-pagination > div {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}
</style>
