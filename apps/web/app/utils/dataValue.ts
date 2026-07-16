export type KnownDataValue = boolean | number | string | string[]

export type DataValue<T extends KnownDataValue = KnownDataValue> =
  | {
      state: "known"
      value: T
      trueLabel?: string
      falseLabel?: string
      suffix?: string
    }
  | {
      state: "unknown"
      label?: string
    }
  | {
      state: "missing"
      label?: string
    }

export interface DataValuePresentation {
  kind: "array" | "boolean" | "missing" | "number" | "string" | "unknown"
  state: DataValue["state"]
  text: string
}

export function presentDataValue(value: DataValue): DataValuePresentation {
  if (value.state === "unknown") {
    return {
      kind: "unknown",
      state: value.state,
      text: value.label ?? "未覆盖",
    }
  }

  if (value.state === "missing") {
    return {
      kind: "missing",
      state: value.state,
      text: value.label ?? "缺失",
    }
  }

  if (typeof value.value === "boolean") {
    return {
      kind: "boolean",
      state: value.state,
      text: value.value
        ? (value.trueLabel ?? "是")
        : (value.falseLabel ?? "否"),
    }
  }

  const suffix = value.suffix ?? ""

  if (typeof value.value === "number") {
    return {
      kind: "number",
      state: value.state,
      text: `${value.value}${suffix}`,
    }
  }

  if (Array.isArray(value.value)) {
    return {
      kind: "array",
      state: value.state,
      text: `${value.value.join("、")}${suffix}`,
    }
  }

  return {
    kind: "string",
    state: value.state,
    text: `${value.value}${suffix}`,
  }
}
