<script setup lang="ts">
import { settleCatalogRequest } from "~/utils/catalogResult"

useHead({
  title: "今日论文 | medpaperhub",
  meta: [
    {
      name: "description",
      content: "医学与生物学重点期刊每日发表、接收、在线优先与趋势情报。",
    },
  ],
})

const client = useCatalogApi()
const {
  data: result,
  status,
  refresh,
} = await useAsyncData("home-catalog", () =>
  settleCatalogRequest(() => client.getHome()),
)

const readyData = computed(() =>
  result.value?.state === "ready" ? result.value.data : undefined,
)
</script>

<template>
  <div class="home-page portal-page">
    <h1 class="visually-hidden">
      medpaperhub 今日论文情报
    </h1>

    <CatalogState
      :result="result"
      :pending="status === 'pending'"
      @retry="refresh"
    />

    <template v-if="readyData">
      <SyncStatus
        :calendar-date="readyData.publication_updates.calendar_date"
        :calendar-timezone="readyData.publication_updates.calendar_timezone"
        :generated-at="readyData.generated_at"
        :jcr-metric-year="readyData.scope.jcr_metric_year"
      />

      <div class="publication-dashboard">
        <div
          class="publication-dashboard__formal"
          data-home-section="formal-publications"
        >
          <BiomedicalDailyBrief
            :brief="readyData.publication_updates.formal_publications_today"
          />
        </div>

        <div class="publication-dashboard__side">
          <div data-home-section="recent-acceptances">
            <PublicationUpdateList
              :collection="readyData.publication_updates.recent_acceptances"
              empty-title="暂无近期接收文章"
              heading-id="recent-acceptances-heading"
              kicker="最近 7 天"
              title="近期接收"
            />
          </div>
          <div data-home-section="recent-online-first">
            <PublicationUpdateList
              :collection="readyData.publication_updates.recent_online_first"
              empty-title="暂无在线优先文章"
              heading-id="recent-online-first-heading"
              kicker="最近 7 天"
              title="在线优先"
            />
          </div>
        </div>
      </div>

      <div data-home-section="trends">
        <EntityMomentumGrid
          compact
          :momentum="readyData.entity_momentum"
        />
      </div>

      <div class="home-activity-grid">
        <div data-home-section="journals">
          <JournalActivityList
            compact
            :activity="readyData.active_journals"
          />
        </div>
        <div data-home-section="subjects">
          <SubjectMomentumGrid
            compact
            :momentum="readyData.subject_momentum"
          />
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.publication-dashboard,
.publication-dashboard__side,
.home-activity-grid {
  display: grid;
  gap: var(--space-5);
  min-width: 0;
}

@media (min-width: 1024px) {
  .home-activity-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
