<script setup lang="ts">
import type { PublicationUpdateCollection } from "~/types/biomedical"

defineProps<{
  brief: PublicationUpdateCollection
}>()
</script>

<template>
  <PublicationUpdateList
    action-label="全部论文"
    action-to="/papers"
    :collection="brief"
    empty-title="今日暂无正式发表文章"
    heading-id="daily-formal-publications-heading"
    kicker="今日"
    title="今日正式发表"
  />
</template>
