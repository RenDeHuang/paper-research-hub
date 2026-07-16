<script setup lang="ts">
import type { CatalogResult } from "~/utils/catalogResult"

const props = withDefaults(
  defineProps<{
    invalidActionTo?: string
    invalidMessage?: string
    invalidTitle?: string
    notFoundActionTo?: string
    notFoundMessage?: string
    notFoundTitle?: string
    pending?: boolean
    result?: CatalogResult<unknown>
  }>(),
  {
    invalidActionTo: undefined,
    invalidMessage: "请清除无效筛选或分页游标后重新请求。",
    invalidTitle: "筛选条件无效",
    notFoundActionTo: undefined,
    notFoundMessage: "请返回列表检查链接，或重新执行搜索。",
    notFoundTitle: "未找到请求的资源",
    pending: false,
    result: undefined,
  },
)

const emit = defineEmits<{
  retry: []
}>()

const requestID = computed(() => {
  if (
    props.result?.state === "waiting"
    || props.result?.state === "not-found"
    || props.result?.state === "invalid"
    || props.result?.state === "error"
  ) {
    return props.result.problem?.request_id
  }
  return undefined
})
</script>

<template>
  <DataState
    v-if="pending && result === undefined"
    state="loading"
    title="正在加载真实目录"
    message="正在从公开 Go API 获取当前已发布目录。"
  />
  <DataState
    v-else-if="result?.state === 'waiting'"
    state="empty"
    title="等待首次同步"
    message="公开目录尚未发布。首次同步完成后，本页会自动展示真实论文、分类与分析结果。"
  />
  <DataState
    v-else-if="result?.state === 'not-found'"
    state="empty"
    :title="notFoundTitle"
    :message="notFoundMessage"
    :action-label="notFoundActionTo ? '返回列表' : undefined"
    :action-to="notFoundActionTo"
  />
  <DataState
    v-else-if="result?.state === 'invalid'"
    state="error"
    :title="invalidTitle"
    :message="`${invalidMessage}${requestID ? ` 请求 ID：${requestID}` : ''}`"
    :action-label="invalidActionTo ? '清除无效条件' : undefined"
    :action-to="invalidActionTo"
  />
  <DataState
    v-else-if="result?.state === 'error'"
    state="error"
    title="暂时无法加载"
    :message="`API 请求未完成。请检查服务状态后重试。${requestID ? ` 请求 ID：${requestID}` : ''}`"
    action-label="重试"
    @action="emit('retry')"
  />
</template>
