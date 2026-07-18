interface RouteLabelSource {
  meta?: Record<string, unknown>
  path: string
}

const fixedRouteLabels: Record<string, string> = {
  "/": "今日",
  "/journals": "期刊",
  "/methods": "Method",
  "/opportunities": "研究机会",
  "/papers": "论文",
  "/subjects": "学科",
  "/topics": "Topic",
  "/trends": "趋势",
}

const dynamicRouteLabels: Array<{
  label: string
  pattern: RegExp
}> = [
  { label: "论文详情", pattern: /^\/papers\/[^/]+$/ },
  { label: "学科", pattern: /^\/subjects\/[^/]+$/ },
  { label: "期刊", pattern: /^\/journals\/[^/]+$/ },
  { label: "Topic", pattern: /^\/topics\/[^/]+$/ },
  { label: "Method", pattern: /^\/methods\/[^/]+$/ },
]

export function resolveRouteLabel(route: RouteLabelSource) {
  const metaTitle = route.meta?.title
  if (typeof metaTitle === "string" && metaTitle.trim().length > 0) {
    return metaTitle.trim()
  }

  const normalizedPath =
    route.path.length > 1 ? route.path.replace(/\/+$/, "") : route.path
  const fixedLabel = fixedRouteLabels[normalizedPath]
  if (fixedLabel) {
    return fixedLabel
  }

  const dynamicLabel = dynamicRouteLabels.find(({ pattern }) =>
    pattern.test(normalizedPath),
  )
  if (dynamicLabel) {
    return dynamicLabel.label
  }

  return `页面：${normalizedPath}`
}
