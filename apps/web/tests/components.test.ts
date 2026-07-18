import { mountSuspended } from "@nuxt/test-utils/runtime"
import { nextTick } from "vue"
import { describe, expect, it, vi } from "vitest"

import AppFooter from "../app/components/AppFooter.vue"
import AppHeader from "../app/components/AppHeader.vue"
import DataState from "../app/components/DataState.vue"
import DiscoveryRail from "../app/components/DiscoveryRail.vue"
import EvidenceBar from "../app/components/EvidenceBar.vue"
import OpportunityMatrix from "../app/components/OpportunityMatrix.vue"
import PaperCard from "../app/components/PaperCard.vue"
import PublicationEventCard from "../app/components/PublicationEventCard.vue"
import PublicationUpdateList from "../app/components/PublicationUpdateList.vue"
import SearchCommand from "../app/components/SearchCommand.vue"
import SyncStatus from "../app/components/SyncStatus.vue"
import TrendSparkline from "../app/components/TrendSparkline.vue"

describe("AppHeader", () => {
  it("renders the daily intelligence navigation and one search utility", async () => {
    const wrapper = await mountSuspended(AppHeader, { route: "/" })
    const links = wrapper.findAll('nav[aria-label="一级导航"] a')

    expect(links.map((link) => link.text())).toEqual([
      "今日",
      "论文",
      "学科",
      "期刊",
      "趋势",
    ])
    expect(links.map((link) => link.attributes("href"))).toEqual([
      "/",
      "/papers",
      "/subjects",
      "/journals",
      "/trends",
    ])
    expect(wrapper.get(".app-header__brand").text()).toBe("medpaperhub")
    expect(wrapper.text()).not.toContain("Paper Research Hub")
    expect(links[0]?.attributes("aria-current")).toBe("page")
    expect(wrapper.get(".app-header__route-name").text()).toBe("今日")
    const utilityLinks = wrapper.findAll(".app-header__utility-link")
    const searchLink = wrapper.get('a[href="/papers#papers-q"]')

    expect(utilityLinks).toHaveLength(1)
    expect(searchLink.text()).toContain("搜索")
    expect(searchLink.attributes("aria-label")).toBe("搜索论文")
    expect(searchLink.attributes("aria-current")).toBeUndefined()
    expect(wrapper.text()).not.toContain("数据状态")
    expect(wrapper.text()).not.toContain("研究机会")
    expect(wrapper.get("button.app-header__menu-button").attributes()).toMatchObject({
      "aria-controls": "primary-navigation",
      "aria-expanded": "false",
      "aria-label": "打开一级导航",
    })
  })

  it("announces the mobile menu state without changing the navigation labels", async () => {
    const wrapper = await mountSuspended(AppHeader, { route: "/" })
    const button = wrapper.get("button.app-header__menu-button")

    await button.trigger("click")

    expect(button.attributes("aria-expanded")).toBe("true")
    expect(button.attributes("aria-label")).toBe("关闭一级导航")
    expect(wrapper.get("#primary-navigation").classes()).toContain("is-open")
    expect(document.documentElement.classList.contains("menu-open")).toBe(true)

    const focus = vi.spyOn(button.element, "focus")
    await wrapper.trigger("keydown", { key: "Escape" })
    await nextTick()

    expect(document.documentElement.classList.contains("menu-open")).toBe(false)
    expect(button.attributes("aria-expanded")).toBe("false")
    expect(focus).toHaveBeenCalledTimes(1)
  })

  it.each([
    ["/papers/paper-1", "论文详情"],
    ["/subjects/oncology", "学科"],
    ["/journals/nature-medicine", "期刊"],
  ])("renders the explicit dynamic route label for %s", async (path, label) => {
    const wrapper = await mountSuspended(AppHeader, { route: path })

    expect(wrapper.get(".app-header__route-name").text()).toBe(label)
    expect(wrapper.text()).not.toContain("当前页面")
  })
})

describe("DiscoveryRail", () => {
  it("renders the four discovery contracts from caller-provided items", async () => {
    const wrapper = await mountSuspended(DiscoveryRail, {
      props: {
        activeJournals: {
          items: [{ label: "Nature Medicine", to: "/journals/nature-medicine" }],
          state: "known",
        },
        trendingSubjects: {
          items: [{ label: "肿瘤学", to: "/subjects/oncology" }],
          state: "known",
        },
        quickFilters: {
          items: [{ label: "JIF ≥ 10", to: "/papers?jif_min=10" }],
          state: "known",
        },
        savedViews: {
          items: [
            {
              label: "我的高影响力视图",
              to: "/papers?saved=high-impact",
            },
          ],
          state: "known",
        },
      },
    })
    const groups = wrapper.findAll("[data-discovery-group]")

    expect(wrapper.get("aside").attributes("aria-label")).toBe("发现导航")
    expect(groups.map((group) => group.attributes("data-discovery-group"))).toEqual([
      "quick-filters",
      "trending-subjects",
      "active-journals",
      "saved-views",
    ])
    expect(groups.map((group) => group.get("h2").text())).toEqual([
      "快捷筛选",
      "学科趋势",
      "活跃期刊",
      "保存视图",
    ])
    expect(wrapper.findAll("a > span").map((label) => label.text())).toEqual([
      "JIF ≥ 10",
      "肿瘤学",
      "Nature Medicine",
      "我的高影响力视图",
    ])
    expect(wrapper.text()).not.toContain("浏览论文")
    expect(wrapper.text()).not.toContain("查看趋势")
    expect(wrapper.text()).not.toContain("研究机会")
  })

  it("keeps known-empty, missing, and unknown discovery states distinct", async () => {
    const wrapper = await mountSuspended(DiscoveryRail, {
      props: {
        activeJournals: {
          label: "活跃期刊尚未生成",
          state: "missing",
        },
        trendingSubjects: {
          label: "学科趋势来源未覆盖",
          state: "unknown",
        },
        quickFilters: {
          items: [],
          state: "known",
        },
        savedViews: {
          items: [],
          state: "known",
        },
      },
    })
    const states = wrapper.findAll("[data-discovery-empty]")

    expect(states.map((state) => state.attributes("data-discovery-empty"))).toEqual([
      "quick-filters",
      "trending-subjects",
      "active-journals",
      "saved-views",
    ])
    expect(
      states.map((state) => state.attributes("data-discovery-state")),
    ).toEqual(["empty", "unknown", "missing", "empty"])
    expect(states.map((state) => state.text())).toEqual([
      "暂无快捷筛选",
      "学科趋势来源未覆盖",
      "活跃期刊尚未生成",
      "暂无保存视图",
    ])
  })
})

describe("SyncStatus", () => {
  it("renders one compact publication scope status row", async () => {
    const wrapper = await mountSuspended(SyncStatus, {
      props: {
        calendarDate: "2026-07-17",
        calendarTimezone: "UTC",
        generatedAt: "2026-07-17T08:30:00Z",
        jcrMetricYear: 2025,
      },
    })
    const values = wrapper.findAll("dd")

    expect(wrapper.attributes("id")).toBe("sync-status")
    expect(wrapper.findAll("dt").map((item) => item.text())).toEqual([
      "日报日期",
      "更新",
      "时区",
      "收录范围",
    ])
    expect(values.map((item) => item.text())).toEqual([
      "2026-07-17",
      "2026-07-17 08:30 UTC",
      "UTC",
      "JCR 2025 · Q1 / JIF ≥ 10",
    ])
  })
})

const publicationPaper = {
  citation_count: { state: "known" as const, value: 27 },
  id: "00000000-0000-4000-8000-000000000101",
  journal: {
    id: "00000000-0000-4000-8000-000000000201",
    slug: "nature-medicine",
    title: "Nature Medicine",
  },
  publication_types: ["Journal Article"],
  title: "Single-cell atlas of treatment response",
}

const publicationEvent = {
  date: "2026-07-18",
  date_precision: "day" as const,
  kind: "electronic_published" as const,
  provenance: {
    normalized_assertion_id: "00000000-0000-4000-8000-000000000301",
    projection_assertion_id: "00000000-0000-4000-8000-000000000302",
    source: "pubmed" as const,
    source_path: "/PubmedArticle/PubmedData/History/PubMedPubDate[2]",
    source_record_id: "00000000-0000-4000-8000-000000000303",
    status_raw: "epublish",
  },
  publication_model: "Electronic",
  publication_status: "epublish",
}

describe("PublicationEventCard", () => {
  it("shows the publication event without exposing internal provenance IDs", async () => {
    const wrapper = await mountSuspended(PublicationEventCard, {
      props: {
        item: {
          event: publicationEvent,
          paper: publicationPaper,
        },
      },
    })

    expect(wrapper.attributes("data-event-kind")).toBe("electronic_published")
    expect(wrapper.get("h3").text()).toBe(publicationPaper.title)
    expect(wrapper.get(`a[href="/papers/${publicationPaper.id}"]`).exists()).toBe(true)
    expect(wrapper.get('a[href="/journals/nature-medicine"]').text()).toBe(
      "Nature Medicine",
    )
    expect(wrapper.get("time").attributes("datetime")).toBe("2026-07-18")
    expect(wrapper.text()).toContain("电子正式发表")
    expect(wrapper.text()).toContain("Journal Article")
    expect(wrapper.text()).toContain("引用 27")
    expect(wrapper.text()).not.toContain(
      publicationEvent.provenance.normalized_assertion_id,
    )
    expect(wrapper.text()).not.toContain(
      publicationEvent.provenance.projection_assertion_id,
    )
  })
})

describe("PublicationUpdateList", () => {
  it("preserves API order and keeps same-paper events distinct", async () => {
    const wrapper = await mountSuspended(PublicationUpdateList, {
      props: {
        collection: {
          analysis: {},
          items: [
            {
              event: publicationEvent,
              paper: publicationPaper,
            },
            {
              event: {
                ...publicationEvent,
                kind: "print_published",
                publication_status: "ppublish",
                provenance: {
                  ...publicationEvent.provenance,
                  status_raw: "ppublish",
                },
              },
              paper: publicationPaper,
            },
          ],
          pagination: {
            has_more: false,
            limit: 2,
            next_cursor: null,
            total: 2,
          },
        },
        emptyTitle: "暂无正式发表",
        headingId: "publication-test-heading",
        kicker: "今日",
        title: "正式发表",
      },
    })

    expect(wrapper.get("h2").attributes("id")).toBe("publication-test-heading")
    expect(
      wrapper.findAll("[data-event-kind]").map((item) =>
        item.attributes("data-event-kind"),
      ),
    ).toEqual(["electronic_published", "print_published"])
    expect(wrapper.findAll("h3").map((heading) => heading.text())).toEqual([
      publicationPaper.title,
      publicationPaper.title,
    ])
  })

  it("renders a short real empty state without example papers", async () => {
    const wrapper = await mountSuspended(PublicationUpdateList, {
      props: {
        collection: {
          analysis: {},
          items: [],
          pagination: {
            has_more: false,
            limit: 0,
            next_cursor: null,
            total: 0,
          },
        },
        emptyTitle: "暂无在线优先文章",
        headingId: "publication-empty-heading",
        title: "在线优先",
      },
    })

    expect(wrapper.get("[data-publication-empty]").text()).toBe(
      "暂无在线优先文章",
    )
    expect(wrapper.find("[data-event-kind]").exists()).toBe(false)
    expect(wrapper.text()).not.toContain("示例")
  })
})

describe("AppFooter", () => {
  it("keeps the footer focused on the publication scope", async () => {
    const wrapper = await mountSuspended(AppFooter, { route: "/" })

    expect(wrapper.get(".app-footer__brand").text()).toBe("medpaperhub")
    expect(wrapper.text()).not.toContain("Paper Research Hub")
    expect(wrapper.text()).toContain("医学与生物学")
    expect(wrapper.text()).toContain("JCR Q1 或 JIF ≥ 10")
    expect(wrapper.find("a").exists()).toBe(false)
    expect(wrapper.text()).not.toContain("研究机会")
  })
})

describe("SearchCommand", () => {
  it("uses conservative biomedical copy for its q-only default search contract", async () => {
    const wrapper = await mountSuspended(SearchCommand)
    const input = wrapper.get('input[name="q"]')

    expect(wrapper.get('label[for="global-search"]').text()).toBe(
      "搜索医学生物学论文",
    )
    expect(wrapper.get("#global-search-helper").text()).toBe(
      "输入标题、作者、期刊或 DOI/PMID 关键词。",
    )
    expect(input.attributes("name")).toBe("q")

    for (const unsupportedClaim of [
      "Topic",
      "Method",
      "Dataset",
      "Benchmark",
      "Venue",
      "MeSH",
      "Publication Type",
    ]) {
      expect(wrapper.get(".search-command").text()).not.toContain(
        unsupportedClaim,
      )
    }
  })

  it("associates a visible label and helper text with the search field", async () => {
    const wrapper = await mountSuspended(SearchCommand, {
      props: {
        helper: "可搜索标题、作者、Topic、Method、Dataset、Benchmark、Venue 和标识符。",
        label: "搜索论文与研究实体",
      },
    })
    const input = wrapper.get('input[name="q"]')

    expect(wrapper.get('label[for="global-search"]').text()).toBe(
      "搜索论文与研究实体",
    )
    expect(input.attributes("id")).toBe("global-search")
    expect(input.attributes("aria-describedby")?.split(" ")).toEqual([
      "global-search-helper",
      "global-search-status",
    ])
    expect(input.attributes("aria-controls")).toBeUndefined()
    expect(wrapper.get("#global-search-helper").text()).toContain("可搜索标题")
    expect(wrapper.get('button[type="submit"]').text()).toBe("搜索")
  })

  it("distinguishes unavailable suggestions from a completed no-match result", async () => {
    const unavailable = await mountSuspended(SearchCommand, {
      props: {
        modelValue: "agent",
        suggestionGroups: [],
      },
    })
    const noMatch = await mountSuspended(SearchCommand, {
      props: {
        modelValue: "agent",
        suggestionGroups: [],
        suggestionsState: "ready",
      },
    })

    expect(unavailable.get("[data-search-state]").attributes(
      "data-search-state",
    )).toBe("unavailable")
    expect(unavailable.get("[data-search-state]").text()).toBe(
      "搜索建议尚未生成",
    )
    expect(noMatch.get("[data-search-state]").attributes(
      "data-search-state",
    )).toBe("no-match")
    expect(noMatch.get("[data-search-state]").text()).toBe("没有匹配结果")
  })

  it("exposes caller-provided entity groups through combobox semantics", async () => {
    const wrapper = await mountSuspended(SearchCommand, {
      props: {
        modelValue: "agent",
        suggestionGroups: [
          {
            id: "papers",
            items: [
              {
                description: "论文",
                entityType: "paper",
                id: "paper-1",
                label: "Agent systems",
                value: "Agent systems",
              },
            ],
            label: "论文",
          },
          {
            id: "methods",
            items: [
              {
                entityType: "method",
                id: "method-1",
                label: "Agentic workflow",
              },
            ],
            label: "方法",
          },
        ],
      },
    })
    const input = wrapper.get('[role="combobox"]')
    const listbox = wrapper.get('[role="listbox"]')
    const groups = wrapper.findAll('[role="group"]')
    const options = wrapper.findAll('[role="option"]')

    expect(input.attributes()).toMatchObject({
      "aria-autocomplete": "list",
      "aria-controls": listbox.attributes("id"),
      "aria-expanded": "true",
    })
    expect(groups.map((group) => group.attributes("aria-labelledby"))).toEqual([
      expect.stringContaining("papers"),
      expect.stringContaining("methods"),
    ])
    expect(options.map((option) => option.text())).toEqual([
      "Agent systems论文论文",
      "Agentic workflowMethod",
    ])
    expect(
      options.map((option) =>
        option.get(".search-command__option-type").text(),
      ),
    ).toEqual(["论文", "Method"])
    expect(options.map((option) => option.attributes("data-entity-type"))).toEqual(
      ["paper", "method"],
    )

    await input.setValue("agent systems")

    expect(wrapper.emitted("update:modelValue")?.at(-1)).toEqual([
      "agent systems",
    ])
    await wrapper.setProps({ modelValue: "caller accepted value" })
    expect(input.element.value).toBe("caller accepted value")
  })

  it("keeps same-name entities distinguishable through visible type labels", async () => {
    const wrapper = await mountSuspended(SearchCommand, {
      props: {
        modelValue: "Agent",
        suggestionGroups: [
          {
            id: "same-name-entities",
            items: [
              {
                entityType: "paper",
                id: "paper-agent",
                label: "Agent",
              },
              {
                entityType: "topic",
                id: "topic-agent",
                label: "Agent",
              },
            ],
            label: "同名实体",
          },
        ],
      },
    })
    const options = wrapper.findAll('[role="option"]')

    expect(options.map((option) => option.get(
      ".search-command__option-label",
    ).text())).toEqual(["Agent", "Agent"])
    expect(options.map((option) => option.get(
      ".search-command__option-type",
    ).text())).toEqual(["论文", "Topic"])
  })

  it("initializes from the URL query and follows later route query changes", async () => {
    const wrapper = await mountSuspended(SearchCommand, {
      route: "/?q=shared%20query",
    })
    const input = wrapper.get<HTMLInputElement>('input[name="q"]')

    expect(input.element.value).toBe("shared query")

    await wrapper.vm.$router.push({ path: "/", query: { q: "restored" } })
    await nextTick()

    expect(input.element.value).toBe("restored")
    expect(wrapper.emitted("update:modelValue")?.at(-1)).toEqual(["restored"])
  })

  it("navigates to the default destination with a trimmed shareable query", async () => {
    const wrapper = await mountSuspended(SearchCommand, { route: "/" })
    const router = wrapper.vm.$router
    const push = vi.spyOn(router, "push")
    router.addRoute({
      component: { template: "<h1>Paper search</h1>" },
      path: "/papers",
    })

    await wrapper.get('input[name="q"]').setValue("  agent systems  ")
    await wrapper.get("form").trigger("submit")
    expect(push).toHaveBeenCalledWith({
      path: "/papers",
      query: { q: "agent systems" },
    })
    await push.mock.results.at(-1)?.value
    await nextTick()

    expect(router.currentRoute.value.fullPath).toBe(
      "/papers?q=agent+systems",
    )
    expect(wrapper.emitted("search")?.at(-1)).toEqual(["agent systems"])
  })

  it("supports a configurable destination without navigating an empty query", async () => {
    const wrapper = await mountSuspended(SearchCommand, {
      props: {
        destination: "/topics",
      },
      route: "/",
    })
    const router = wrapper.vm.$router
    const push = vi.spyOn(router, "push")
    router.addRoute({
      component: { template: "<h1>Topic search</h1>" },
      path: "/topics",
    })
    const input = wrapper.get('input[name="q"]')

    await input.setValue("   ")
    await wrapper.get("form").trigger("submit")
    await nextTick()

    expect(router.currentRoute.value.fullPath).toBe("/")
    expect(wrapper.emitted("search")).toBeUndefined()

    await input.setValue("causal inference")
    await wrapper.get("form").trigger("submit")
    expect(push).toHaveBeenLastCalledWith({
      path: "/topics",
      query: { q: "causal inference" },
    })
    await push.mock.results.at(-1)?.value
    await nextTick()

    expect(router.currentRoute.value.fullPath).toBe(
      "/topics?q=causal+inference",
    )
  })

  it("restores input focus after a pointer selects a suggestion", async () => {
    const wrapper = await mountSuspended(SearchCommand, {
      attachTo: document.body,
      props: {
        modelValue: "agent",
        suggestionGroups: [
          {
            id: "entities",
            items: [
              {
                entityType: "topic",
                id: "topic-1",
                label: "Agent",
              },
            ],
            label: "研究实体",
          },
        ],
      },
    })
    const input = wrapper.get<HTMLInputElement>('[role="combobox"]')
    const option = wrapper.get<HTMLButtonElement>('[role="option"]')

    option.element.focus()
    expect(document.activeElement).toBe(option.element)
    await option.trigger("click")
    await nextTick()

    expect(document.activeElement).toBe(input.element)
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it("supports option navigation, selection, and Escape dismissal", async () => {
    const wrapper = await mountSuspended(SearchCommand, {
      attachTo: document.body,
      props: {
        modelValue: "agent",
        suggestionGroups: [
          {
            id: "entities",
            items: [
              {
                entityType: "topic",
                id: "topic-1",
                label: "Agent",
              },
              {
                entityType: "method",
                id: "method-1",
                label: "Agentic RAG",
                value: "agentic-rag",
              },
            ],
            label: "研究实体",
          },
        ],
      },
    })
    const input = wrapper.get('[role="combobox"]')
    const options = wrapper.findAll('[role="option"]')

    ;(input.element as HTMLInputElement).focus()
    await input.trigger("keydown", { key: "ArrowDown" })
    expect(input.attributes("aria-activedescendant")).toBe(
      options[0]?.attributes("id"),
    )
    expect(options[0]?.attributes("aria-selected")).toBe("true")

    await input.trigger("keydown", { key: "ArrowDown" })
    expect(input.attributes("aria-activedescendant")).toBe(
      options[1]?.attributes("id"),
    )

    await input.trigger("keydown", { key: "ArrowUp" })
    expect(input.attributes("aria-activedescendant")).toBe(
      options[0]?.attributes("id"),
    )

    await input.trigger("keydown", { key: "ArrowDown" })
    await input.trigger("keydown", { key: "Enter" })

    expect(wrapper.emitted("select")?.at(-1)?.[0]).toMatchObject({
      entityType: "method",
      id: "method-1",
    })
    expect(wrapper.emitted("update:modelValue")?.at(-1)).toEqual([
      "agentic-rag",
    ])
    expect(input.attributes("aria-expanded")).toBe("false")

    await input.trigger("focus")
    await input.trigger("keydown", { key: "ArrowDown" })
    await input.trigger("keydown", { key: "Escape" })

    expect(input.attributes("aria-expanded")).toBe("false")
    expect(input.attributes("aria-activedescendant")).toBeUndefined()
    expect(document.activeElement).toBe(input.element)
    wrapper.unmount()
  })

  it.each([
    {
      expected: "请输入关键词",
      props: { modelValue: "", suggestionGroups: [] },
      state: "empty-query",
    },
    {
      expected: "正在加载搜索建议",
      props: { loading: true, modelValue: "agent", suggestionGroups: [] },
      state: "loading",
    },
    {
      expected: "搜索服务暂不可用",
      props: {
        error: "搜索服务暂不可用",
        modelValue: "agent",
        suggestionGroups: [],
      },
      state: "error",
    },
    {
      expected: "没有匹配结果",
      props: {
        modelValue: "agent",
        suggestionGroups: [],
        suggestionsState: "ready",
      },
      state: "no-match",
    },
  ])("renders the $state command state independently", async ({
    expected,
    props,
    state,
  }) => {
    const wrapper = await mountSuspended(SearchCommand, { props })
    const status = wrapper.get(`[data-search-state="${state}"]`)

    expect(status.text()).toContain(expected)
    expect(wrapper.find('[role="listbox"]').exists()).toBe(false)
    if (state === "error") {
      expect(status.attributes("role")).toBe("alert")
    } else {
      expect(status.attributes("role")).toBe("status")
    }
  })
})

describe("PaperCard", () => {
  it("keeps unknown, missing, false, and zero semantically distinct", async () => {
    const wrapper = await mountSuspended(PaperCard, {
      props: {
        evidence: [
          {
            label: "开放代码",
            value: {
              falseLabel: "否",
              state: "known",
              trueLabel: "是",
              value: false,
            },
          },
          {
            label: "引用数",
            value: { state: "known", value: 0 },
          },
          {
            label: "PMC 覆盖",
            value: { state: "unknown" },
          },
          {
            label: "摘要",
            value: { state: "missing" },
          },
        ],
        sourceType: { state: "known", value: "期刊论文" },
        status: { label: "已收录", tone: "info" },
        title: "可复用论文卡片",
      },
    })
    const values = wrapper.findAll(".paper-card__evidence-value")

    expect(values.map((value) => value.text())).toEqual([
      "否",
      "0",
      "未覆盖",
      "缺失",
    ])
    expect(values.map((value) => value.attributes("data-value-state"))).toEqual([
      "known",
      "known",
      "unknown",
      "missing",
    ])
    expect(values.map((value) => value.attributes("data-value-kind"))).toEqual([
      "boolean",
      "number",
      "unknown",
      "missing",
    ])
  })

  it.each([
    ["retracted", "已撤稿"],
    ["withdrawn", "已撤回"],
    ["rejected", "已拒绝"],
    ["superseded", "已被替代"],
    ["excluded", "已排除"],
  ] as const)(
    "makes the %s lifecycle visibly unavailable for recommendation",
    async (code, label) => {
      const wrapper = await mountSuspended(PaperCard, {
        props: {
          status: {
            code,
            description: `${label}的来源说明`,
            label,
            tone: "negative",
          },
          title: `${label}论文`,
        },
      })

      expect(wrapper.attributes("data-paper-status")).toBe(code)
      expect(wrapper.classes()).toContain("paper-card--not-recommendable")
      expect(wrapper.get(".paper-card__status-description").text()).toBe(
        `${label}的来源说明`,
      )
      expect(wrapper.get(".paper-card__recommendation-note").text()).toMatch(
        /不会进入推荐榜|当前不可推荐/,
      )
      expect(wrapper.get(".status-badge").attributes("title")).toBeUndefined()
    },
  )

  it("renders both title and detail destinations as explicit links", async () => {
    const wrapper = await mountSuspended(PaperCard, {
      props: {
        detailLabel: "核查论文证据",
        detailTo: "/papers/paper-1",
        title: "有详情入口的论文",
      },
    })
    const links = wrapper.findAll("a")

    expect(links).toHaveLength(2)
    expect(links.map((link) => link.attributes("href"))).toEqual([
      "/papers/paper-1",
      "/papers/paper-1",
    ])
  })
})

describe("EvidenceBar", () => {
  it("renders a known zero as data rather than as a missing bar", async () => {
    const wrapper = await mountSuspended(EvidenceBar, {
      props: {
        label: "代码可得性",
        value: { state: "known", value: 0 },
      },
    })

    expect(wrapper.attributes("data-value-state")).toBe("known")
    expect(wrapper.get(".evidence-bar__value").text()).toBe("0%")
    expect(wrapper.get("[data-zero-marker]").exists()).toBe(true)
    expect(wrapper.find(".evidence-bar__missing").exists()).toBe(false)
  })

  it("distinguishes missing evidence from unknown coverage", async () => {
    const missing = await mountSuspended(EvidenceBar, {
      props: {
        label: "可复现性",
        value: { state: "missing" },
      },
    })
    const unknown = await mountSuspended(EvidenceBar, {
      props: {
        label: "数据可得性",
        value: { state: "unknown" },
      },
    })

    expect(missing.attributes("data-value-state")).toBe("missing")
    expect(missing.get(".evidence-bar__value").text()).toBe("缺失")
    expect(unknown.attributes("data-value-state")).toBe("unknown")
    expect(unknown.get(".evidence-bar__value").text()).toBe("未覆盖")
  })
})

describe("TrendSparkline", () => {
  it("provides a visible summary and complete table alternative", async () => {
    const wrapper = await mountSuspended(TrendSparkline, {
      props: {
        points: [
          { label: "2026-07-13", state: "known", value: 4 },
          { label: "2026-07-14", state: "known", value: 0 },
          { label: "2026-07-15", state: "unknown" },
          { label: "2026-07-16", state: "missing" },
        ],
        summary: "该序列从 4 降至 0，后续一天未覆盖、一天缺失。",
        title: "四日论文趋势",
        unit: "篇",
      },
    })

    expect(wrapper.get(".trend-sparkline__summary").text()).toContain(
      "该序列从 4 降至 0",
    )
    expect(wrapper.get("svg").attributes("aria-label")).toBe(
      "该序列从 4 降至 0，后续一天未覆盖、一天缺失。",
    )
    expect(wrapper.get(".data-table-disclosure summary").text()).toBe(
      "查看数据表",
    )
    expect(
      wrapper.findAll("tbody [data-value-state]").map((cell) => cell.text()),
    ).toEqual(["4篇", "0篇", "未覆盖", "缺失"])
  })

  it("uses empty only when no time points exist and keeps the table entry", async () => {
    const wrapper = await mountSuspended(TrendSparkline, {
      props: {
        points: [],
        summary: "尚无时间点。",
        title: "空趋势",
      },
    })

    expect(wrapper.get('.data-state[data-state="empty"]').exists()).toBe(true)
    expect(wrapper.find("svg").exists()).toBe(false)
    expect(wrapper.get(".data-table-disclosure summary").text()).toBe(
      "查看数据表",
    )
    expect(wrapper.findAll("tbody tr")).toHaveLength(0)
  })

  it("reports insufficient coverage when every time point is unknown", async () => {
    const wrapper = await mountSuspended(TrendSparkline, {
      props: {
        points: [
          { label: "第一期", state: "unknown" },
          { label: "第二期", state: "unknown" },
        ],
        summary: "两个时间点均未覆盖。",
        title: "未覆盖趋势",
      },
    })

    expect(
      wrapper.get('.data-state[data-state="insufficient"]').exists(),
    ).toBe(true)
    expect(wrapper.find("svg").exists()).toBe(false)
    expect(
      wrapper.findAll("tbody [data-value-state]").map((cell) => cell.text()),
    ).toEqual(["未覆盖", "未覆盖"])
  })

  it("reports missing data when unplottable points include an expected value", async () => {
    const wrapper = await mountSuspended(TrendSparkline, {
      props: {
        points: [
          { label: "第一期", state: "unknown" },
          { label: "第二期", state: "missing" },
        ],
        summary: "一期未覆盖，一期数据缺失。",
        title: "缺失趋势",
      },
    })

    expect(wrapper.get('.data-state[data-state="missing"]').exists()).toBe(true)
    expect(wrapper.find("svg").exists()).toBe(false)
    expect(
      wrapper.findAll("tbody [data-value-state]").map((cell) => cell.text()),
    ).toEqual(["未覆盖", "缺失"])
  })
})

describe("OpportunityMatrix", () => {
  it("encodes every recommendation with shape, text, and a data table", async () => {
    const wrapper = await mountSuspended(OpportunityMatrix, {
      props: {
        points: [
          {
            id: "worth",
            label: "方向 A",
            missingSignals: [],
            status: "worth-pursuing",
            x: { state: "known", value: 20 },
            y: { state: "known", value: 80 },
          },
          {
            id: "caution",
            label: "方向 B",
            missingSignals: [],
            status: "proceed-with-caution",
            x: { state: "known", value: 40 },
            y: { state: "known", value: 60 },
          },
          {
            id: "not-now",
            label: "方向 C",
            missingSignals: [],
            status: "not-recommended-now",
            x: { state: "known", value: 80 },
            y: { state: "known", value: 20 },
          },
          {
            id: "insufficient",
            label: "方向 D",
            missingSignals: ["外部验证", "长期随访"],
            status: "insufficient-evidence",
            x: { state: "known", value: 55 },
            y: { state: "known", value: 45 },
          },
        ],
        summary: "四个方向分别覆盖四种独立机会状态。",
        title: "研究机会概览",
        xAxisLabel: "竞争密度",
        yAxisLabel: "增长信号",
      },
    })

    expect(
      wrapper
        .get(".opportunity-matrix__desktop-chart")
        .attributes("aria-label"),
    ).toBe(
      "四个方向分别覆盖四种独立机会状态。",
    )
    expect(
      wrapper
        .findAll(
          ".opportunity-matrix__desktop-chart [data-opportunity-shape]",
        )
        .map((node) => ({
          shape: node.attributes("data-opportunity-shape"),
          status: node.attributes("data-status"),
        })),
    ).toEqual([
      { shape: "circle", status: "worth-pursuing" },
      { shape: "diamond", status: "proceed-with-caution" },
      { shape: "square", status: "not-recommended-now" },
      { shape: "hollow-circle", status: "insufficient-evidence" },
    ])
    expect(wrapper.get(".opportunity-matrix__overview summary").text()).toBe(
      "查看矩阵概览",
    )
    expect(wrapper.get(".opportunity-matrix__table summary").text()).toBe(
      "查看数据表",
    )
    expect(
      wrapper.findAll("tbody .opportunity-matrix__status").map((cell) =>
        cell.text(),
      ),
    ).toEqual(["值得做", "谨慎做", "当前不建议做", "证据不足"])
    expect(
      wrapper
        .findAll(".opportunity-matrix__mobile-groups > section")
        .map((group) => group.attributes("data-opportunity-group")),
    ).toEqual([
      "worth-pursuing",
      "proceed-with-caution",
      "not-recommended-now",
      "insufficient-evidence",
    ])
  })

  it("plots known zero but never invents unknown or missing coordinates", async () => {
    const wrapper = await mountSuspended(OpportunityMatrix, {
      props: {
        points: [
          {
            id: "missing-both",
            label: "方向 D",
            missingSignals: ["竞争密度", "增长信号"],
            status: "insufficient-evidence",
            x: { state: "missing" },
            y: { state: "unknown" },
          },
          {
            id: "unknown-x",
            label: "方向 B",
            missingSignals: ["竞争基线"],
            status: "proceed-with-caution",
            x: { state: "unknown" },
            y: { state: "known", value: 10 },
          },
          {
            id: "known-zero",
            label: "方向 A",
            missingSignals: [],
            status: "worth-pursuing",
            x: { state: "known", value: 0 },
            y: { state: "known", value: 0 },
          },
          {
            id: "missing-y",
            label: "方向 C",
            missingSignals: ["增长信号"],
            status: "not-recommended-now",
            x: { state: "known", value: 20 },
            y: { state: "missing" },
          },
        ],
        summary: "仅方向 A 具备完整坐标。",
        title: "坐标覆盖测试",
        xAxisLabel: "竞争密度",
        yAxisLabel: "增长信号",
      },
    })
    const plotted = wrapper.findAll(
      ".opportunity-matrix__desktop-chart [data-opportunity-shape]",
    )
    const coordinateCells = wrapper.findAll(
      "tbody [data-coordinate-value]",
    )

    expect(plotted).toHaveLength(1)
    expect(plotted[0]?.attributes("data-status")).toBe("worth-pursuing")
    expect(wrapper.get("[data-unplottable-count]").text()).toContain(
      "3 个方向",
    )
    expect(wrapper.get("[data-unplottable-count]").text()).toContain(
      "2 个方向存在预期坐标缺失",
    )
    expect(wrapper.get("[data-unplottable-count]").text()).toContain(
      "1 个方向的坐标未覆盖",
    )
    expect(coordinateCells.map((cell) => cell.text())).toEqual([
      "缺失",
      "未覆盖",
      "未覆盖",
      "10",
      "0",
      "0",
      "20",
      "缺失",
    ])
    expect(
      coordinateCells.map((cell) => cell.attributes("data-value-state")),
    ).toEqual([
      "missing",
      "unknown",
      "unknown",
      "known",
      "known",
      "known",
      "known",
      "missing",
    ])
    expect(
      wrapper
        .findAll(".opportunity-matrix__mobile-groups > section")
        .map((group) => group.attributes("data-opportunity-group")),
    ).toEqual([
      "worth-pursuing",
      "proceed-with-caution",
      "not-recommended-now",
      "insufficient-evidence",
    ])
    expect(
      wrapper
        .findAll(".opportunity-matrix__desktop-chart [data-axis-tick]")
        .map((tick) => tick.text()),
    ).toEqual(["0", "50", "100", "0", "50", "100"])
  })

  it("uses unknown rather than insufficient when every coordinate is uncovered", async () => {
    const wrapper = await mountSuspended(OpportunityMatrix, {
      props: {
        points: [
          {
            id: "unknown",
            label: "方向 U",
            missingSignals: ["竞争密度", "增长信号"],
            status: "insufficient-evidence",
            x: { state: "unknown" },
            y: { state: "unknown" },
          },
        ],
        summary: "坐标来源尚未覆盖。",
        title: "坐标覆盖未知",
        xAxisLabel: "竞争密度",
        yAxisLabel: "增长信号",
      },
    })

    expect(
      wrapper.get(".opportunity-matrix__desktop-state").attributes(
        "data-state",
      ),
    ).toBe("unknown")
    expect(wrapper.get("[data-unplottable-count]").text()).toContain(
      "1 个方向的坐标未覆盖",
    )
    expect(wrapper.get("[data-unplottable-count]").text()).not.toContain(
      "坐标缺失",
    )
  })

  it("preserves caller signal order across matrix details and accessible alternatives", async () => {
    const wrapper = await mountSuspended(OpportunityMatrix, {
      props: {
        points: [
          {
            id: "insufficient-known",
            label: "方向 E",
            missingSignals: ["外部验证", "长期随访"],
            status: "insufficient-evidence",
            x: { state: "known", value: 30 },
            y: { state: "known", value: 40 },
          },
          {
            id: "insufficient-unplottable",
            label: "方向 F",
            missingSignals: ["竞争密度", "增长信号"],
            status: "insufficient-evidence",
            x: { state: "missing" },
            y: { state: "unknown" },
          },
          {
            id: "complete",
            label: "方向 G",
            missingSignals: [],
            status: "worth-pursuing",
            x: { state: "known", value: 60 },
            y: { state: "known", value: 70 },
          },
        ],
        summary: "三个方向包含可核查的缺失信号。",
        title: "缺失信号测试",
        xAxisLabel: "竞争密度",
        yAxisLabel: "增长信号",
      },
    })

    expect(
      wrapper
        .get(
          '[data-opportunity-group="insufficient-evidence"] [data-opportunity-id="insufficient-unplottable"] [data-missing-signals]',
        )
        .text(),
    ).toBe("竞争密度；增长信号")
    expect(
      wrapper
        .get(
          '.opportunity-matrix__overview [data-opportunity-detail="insufficient-unplottable"] [data-missing-signals]',
        )
        .text(),
    ).toBe("竞争密度；增长信号")
    expect(
      wrapper
        .get(
          '.opportunity-matrix__desktop-details [data-opportunity-detail="insufficient-known"] [data-missing-signals]',
        )
        .text(),
    ).toBe("外部验证；长期随访")
    expect(wrapper.findAll("thead th").map((cell) => cell.text())).toEqual([
      "研究方向",
      "状态",
      "竞争密度",
      "增长信号",
      "缺失信号",
    ])
    expect(
      wrapper
        .findAll("tbody [data-missing-signals]")
        .map((cell) => cell.text()),
    ).toEqual(["外部验证；长期随访", "竞争密度；增长信号", "无"])
    expect(
      wrapper
        .findAll(
          '.opportunity-matrix__desktop-chart [role="graphics-symbol"]',
        )[0]
        ?.attributes("aria-label"),
    ).toContain("缺失信号 外部验证；长期随访")
  })

  it("keeps an omitted missing-signal collection distinct from an explicit empty collection", async () => {
    const wrapper = await mountSuspended(OpportunityMatrix, {
      props: {
        points: [
          {
            id: "uncovered-signals",
            label: "方向 H",
            status: "proceed-with-caution",
            x: { state: "known", value: 40 },
            y: { state: "known", value: 60 },
          },
          {
            id: "no-missing-signals",
            label: "方向 I",
            missingSignals: [],
            status: "worth-pursuing",
            x: { state: "known", value: 20 },
            y: { state: "known", value: 80 },
          },
        ],
        summary: "区分未覆盖与明确空集合。",
        title: "缺失信号覆盖测试",
        xAxisLabel: "竞争密度",
        yAxisLabel: "增长信号",
      },
    })

    const signalCells = wrapper.findAll("tbody [data-missing-signals]")
    expect(signalCells.map((cell) => cell.text())).toEqual(["未覆盖", "无"])
    expect(
      signalCells.map((cell) => cell.attributes("data-value-state")),
    ).toEqual(["unknown", "known"])
  })

  it("renders an explicit empty state without a matrix overview", async () => {
    const wrapper = await mountSuspended(OpportunityMatrix, {
      props: {
        points: [],
        summary: "尚无方向。",
        title: "空机会矩阵",
        xAxisLabel: "竞争密度",
        yAxisLabel: "增长信号",
      },
    })

    expect(wrapper.get('.data-state[data-state="empty"]').exists()).toBe(true)
    expect(wrapper.find(".opportunity-matrix__overview").exists()).toBe(false)
    expect(wrapper.find("svg").exists()).toBe(false)
    expect(wrapper.get(".opportunity-matrix__table summary").text()).toBe(
      "查看数据表",
    )
  })
})

describe("DataState", () => {
  it.each([
    { label: "尚无数据", role: "region", state: "empty" },
    { label: "加载失败", role: "alert", state: "error" },
    { label: "证据不足", role: "region", state: "insufficient" },
    { label: "加载中", role: "status", state: "loading" },
    { label: "数据缺失", role: "region", state: "missing" },
    { label: "访问受限", role: "region", state: "restricted" },
    { label: "数据可能过时", role: "region", state: "stale" },
    { label: "覆盖未知", role: "region", state: "unknown" },
  ] as const)("renders the $state state with explicit semantics", async ({
    label,
    role,
    state,
  }) => {
    const wrapper = await mountSuspended(DataState, {
      props: {
        message: `${state} message`,
        state,
        title: `${state} title`,
      },
    })

    expect(wrapper.attributes("data-state")).toBe(state)
    expect(wrapper.attributes("role")).toBe(role)
    expect(wrapper.get("h2").text()).toBe(`${state} title`)
    expect(wrapper.text()).toContain(`${state} message`)
    expect(wrapper.get(".data-state__label").text()).toBe(label)
    expect(wrapper.attributes("aria-live")).toBe(
      state === "loading" ? "polite" : undefined,
    )
  })

  it("supports either a navigation action or a caller-handled action", async () => {
    const linkState = await mountSuspended(DataState, {
      props: {
        actionLabel: "查看同步说明",
        actionTo: "/sync",
        state: "missing",
        title: "尚未同步",
      },
    })
    const buttonState = await mountSuspended(DataState, {
      props: {
        actionLabel: "重试",
        state: "error",
        title: "同步失败",
      },
    })

    expect(linkState.get("a").attributes("href")).toBe("/sync")
    await buttonState.get("button").trigger("click")
    expect(buttonState.emitted("action")).toHaveLength(1)
  })

  it("renders an explicit empty state without inventing records", async () => {
    const wrapper = await mountSuspended(DataState, {
      props: {
        message: "数据接入后将在此显示真实论文与分析结果。",
        state: "empty",
        title: "等待 API 数据",
      },
    })

    expect(wrapper.attributes("data-state")).toBe("empty")
    expect(wrapper.get("h2").text()).toBe("等待 API 数据")
    expect(wrapper.text()).toContain("数据接入后将在此显示真实论文与分析结果。")
    expect(wrapper.find("a, button").exists()).toBe(false)
  })
})
