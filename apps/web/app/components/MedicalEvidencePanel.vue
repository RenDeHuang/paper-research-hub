<script setup lang="ts">
import type { PaperDetail } from "~/types/catalog"

const props = defineProps<{
  paper: PaperDetail
}>()

const meshHeadings = computed(() =>
  props.paper.mesh_headings.state === "known"
    ? props.paper.mesh_headings.value
    : undefined,
)
const publicationTypes = computed(() =>
  props.paper.publication_types_state.state === "known"
    ? props.paper.publication_types_state.value
    : undefined,
)
</script>

<template>
  <section
    class="detail-section medical-evidence"
    data-medical-evidence
    aria-labelledby="medical-evidence-heading"
  >
    <p class="section-kicker">
      医学语义
    </p>
    <h2 id="medical-evidence-heading">
      医学主题证据
    </h2>

    <div class="medical-evidence__block">
      <h3>MeSH</h3>
      <DataState
        v-if="meshHeadings === undefined"
        state="missing"
        title="MeSH 证据缺失"
        message="本次发布没有可展示的 MeSH Descriptor 与 Qualifier 证据。"
      />
      <DataState
        v-else-if="meshHeadings.length === 0"
        state="empty"
        title="未标注 MeSH"
        message="来源记录已完成 MeSH 投影，但没有返回 Descriptor。"
      />
      <ol v-else class="mesh-list">
        <li
          v-for="heading in meshHeadings"
          :key="`${heading.descriptor_ui}:${heading.source_path}`"
          class="mesh-card"
        >
          <h4>MeSH Descriptor</h4>
          <dl class="detail-list">
            <div>
              <dt>名称</dt>
              <dd>{{ heading.label }}</dd>
            </div>
            <div>
              <dt>Descriptor UI</dt>
              <dd>{{ heading.descriptor_ui }}</dd>
            </div>
            <div>
              <dt>Major Topic</dt>
              <dd
                :data-major-topic="String(heading.is_major_topic)"
              >
                {{ heading.is_major_topic ? "是" : "否" }}
              </dd>
            </div>
            <div>
              <dt>source_path</dt>
              <dd class="medical-evidence__source-path">
                {{ heading.source_path }}
              </dd>
            </div>
          </dl>

          <div class="medical-evidence__qualifiers">
            <h5>Qualifier</h5>
            <p v-if="heading.qualifiers.length === 0">
              未标注 Qualifier
            </p>
            <ul v-else class="qualifier-list">
              <li
                v-for="qualifier in heading.qualifiers"
                :key="`${qualifier.qualifier_ui}:${qualifier.source_path}`"
              >
                <dl class="detail-list">
                  <div>
                    <dt>名称</dt>
                    <dd>{{ qualifier.label }}</dd>
                  </div>
                  <div>
                    <dt>Qualifier UI</dt>
                    <dd>{{ qualifier.qualifier_ui }}</dd>
                  </div>
                  <div>
                    <dt>Major Topic</dt>
                    <dd
                      :data-major-topic="String(qualifier.is_major_topic)"
                    >
                      {{ qualifier.is_major_topic ? "是" : "否" }}
                    </dd>
                  </div>
                  <div>
                    <dt>source_path</dt>
                    <dd class="medical-evidence__source-path">
                      {{ qualifier.source_path }}
                    </dd>
                  </div>
                </dl>
              </li>
            </ul>
          </div>
        </li>
      </ol>
    </div>

    <div class="medical-evidence__block">
      <h3>Publication Type</h3>
      <DataState
        v-if="publicationTypes === undefined"
        state="missing"
        title="Publication Type 缺失"
        message="本次发布没有可展示的 Publication Type 证据。"
      />
      <DataState
        v-else-if="publicationTypes.length === 0"
        state="empty"
        title="未标注 Publication Type"
        message="来源记录已完成 Publication Type 投影，但没有返回分类。"
      />
      <ul v-else class="publication-type-list">
        <li
          v-for="publicationType in publicationTypes"
          :key="publicationType"
        >
          {{ publicationType }}
        </li>
      </ul>
    </div>
  </section>
</template>

<style scoped>
.medical-evidence {
  gap: var(--space-4);
}

.medical-evidence__block,
.mesh-card,
.medical-evidence__qualifiers {
  display: grid;
  gap: var(--space-3);
}

.medical-evidence h3,
.medical-evidence h4,
.medical-evidence h5,
.medical-evidence__qualifiers p {
  margin: 0;
}

.medical-evidence h3 {
  color: var(--color-text-strong);
  font-size: var(--text-base-size);
}

.medical-evidence h4,
.medical-evidence h5 {
  color: var(--color-text-strong);
  font-size: var(--text-sm-size);
}

.mesh-list,
.qualifier-list,
.publication-type-list {
  display: grid;
  gap: var(--space-3);
  margin: 0;
  padding: 0;
  list-style: none;
}

.mesh-card {
  min-width: 0;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
}

.qualifier-list > li {
  min-width: 0;
  padding-left: var(--space-3);
  border-left: 3px solid var(--color-border);
}

.publication-type-list {
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr));
}

.publication-type-list > li {
  padding: var(--space-3);
  border-radius: var(--radius-sm);
  background: var(--color-surface-subtle);
  color: var(--color-text-strong);
  font-weight: 700;
}

.medical-evidence__source-path {
  font-family: var(--font-data);
  font-size: var(--text-sm-size);
  font-weight: 600;
  overflow-wrap: anywhere;
}
</style>
