import { mountSuspended } from "@nuxt/test-utils/runtime"
import { describe, expect, it } from "vitest"

import AppFooter from "../app/components/AppFooter.vue"
import AppHeader from "../app/components/AppHeader.vue"
import DataState from "../app/components/DataState.vue"
import EvidenceBar from "../app/components/EvidenceBar.vue"
import OpportunityMatrix from "../app/components/OpportunityMatrix.vue"
import PaperCard from "../app/components/PaperCard.vue"
import SearchCommand from "../app/components/SearchCommand.vue"
import TrendSparkline from "../app/components/TrendSparkline.vue"

describe("AppHeader", () => {
  it("renders the fixed primary navigation and an accessible search entry", async () => {
    const wrapper = await mountSuspended(AppHeader, { route: "/" })
    const links = wrapper.findAll('nav[aria-label="一级导航"] a')

    expect(links.map((link) => link.text())).toEqual([
      "首页",
      "论文",
      "趋势",
      "研究机会",
    ])
    expect(links.map((link) => link.attributes("href"))).toEqual([
      "/",
      "/papers",
      "/trends",
      "/opportunities",
    ])
    expect(links[0]?.attributes("aria-current")).toBe("page")
    const searchLink = wrapper.get('a[href="/#global-search"]')

    expect(searchLink.text()).toContain("搜索")
    expect(searchLink.attributes("aria-current")).toBeUndefined()
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

    await wrapper.trigger("keydown", { key: "Escape" })

    expect(document.documentElement.classList.contains("menu-open")).toBe(false)
    expect(button.attributes("aria-expanded")).toBe("false")
  })
})

describe("AppFooter", () => {
  it("links back to search without announcing the fragment as the current page", async () => {
    const wrapper = await mountSuspended(AppFooter, { route: "/" })
    const searchLink = wrapper.get('a[href="/#global-search"]')

    expect(searchLink.text()).toBe("返回搜索")
    expect(searchLink.attributes("aria-current")).toBeUndefined()
  })
})

describe("SearchCommand", () => {
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
    expect(input.attributes("aria-describedby")).toBe("global-search-helper")
    expect(wrapper.get("#global-search-helper").text()).toContain("可搜索标题")
    expect(wrapper.get('button[type="submit"]').text()).toBe("搜索")
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
    expect(wrapper.get("details summary").text()).toBe("查看数据表")
    expect(
      wrapper.findAll("tbody [data-value-state]").map((cell) => cell.text()),
    ).toEqual(["4篇", "0篇", "未覆盖", "缺失"])
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
            status: "worth-pursuing",
            x: 20,
            y: 80,
          },
          {
            id: "caution",
            label: "方向 B",
            status: "proceed-with-caution",
            x: 40,
            y: 60,
          },
          {
            id: "not-now",
            label: "方向 C",
            status: "not-recommended-now",
            x: 80,
            y: 20,
          },
          {
            id: "insufficient",
            label: "方向 D",
            status: "insufficient-evidence",
            x: 55,
            y: 45,
          },
        ],
        summary: "四个方向分别覆盖四种独立机会状态。",
        title: "研究机会概览",
        xAxisLabel: "竞争密度",
        yAxisLabel: "增长信号",
      },
    })

    expect(wrapper.get("svg").attributes("aria-label")).toBe(
      "四个方向分别覆盖四种独立机会状态。",
    )
    expect(
      wrapper.findAll("[data-opportunity-shape]").map((node) => ({
        shape: node.attributes("data-opportunity-shape"),
        status: node.attributes("data-status"),
      })),
    ).toEqual([
      { shape: "circle", status: "worth-pursuing" },
      { shape: "diamond", status: "proceed-with-caution" },
      { shape: "square", status: "not-recommended-now" },
      { shape: "hollow-circle", status: "insufficient-evidence" },
    ])
    expect(wrapper.get("details summary").text()).toBe("查看数据表")
    expect(
      wrapper.findAll("tbody .opportunity-matrix__status").map((cell) =>
        cell.text(),
      ),
    ).toEqual(["值得做", "谨慎做", "当前不建议做", "证据不足"])
    expect(
      wrapper.findAll(".opportunity-matrix__mobile-list li").map((item) =>
        item.text(),
      ),
    ).toEqual([
      "方向 A 值得做竞争密度 20增长信号 80",
      "方向 B 谨慎做竞争密度 40增长信号 60",
      "方向 C 当前不建议做竞争密度 80增长信号 20",
      "方向 D 证据不足竞争密度 55增长信号 45",
    ])
  })
})

describe("DataState", () => {
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
