// @vitest-environment node

import { readFileSync } from "node:fs"
import { fileURLToPath } from "node:url"

import { describe, expect, it } from "vitest"
import { parse } from "yaml"

const requestIDParameterRef = "#/components/parameters/RequestID"
const discoveryResponseRef = "#/components/schemas/DiscoveryResponse"
const taxonomyItemRef = "#/components/schemas/TaxonomyItem"
const taxonomySlugRef = "#/components/schemas/TaxonomySlug"
const openAPIPath = fileURLToPath(
  new URL("../../../contracts/openapi.yaml", import.meta.url),
)
const document = parse(readFileSync(openAPIPath, "utf8")) as unknown

describe("OpenAPI contract", () => {
  it("parses and resolves every local reference", () => {
    expect(isRecord(document)).toBe(true)
    visitReferences(document, (reference) => {
      expect(() => resolveLocalReference(document, reference)).not.toThrow()
    })
  })

  it("reuses the strict Request ID header parameter on every public operation", () => {
    const parameter = getRecord(document, "components", "parameters", "RequestID")
    expect(parameter).toMatchObject({
      name: "X-Request-ID",
      in: "header",
      required: false,
    })

    const schema = resolveSchema(document, parameter.schema)
    expect(schema).toMatchObject({
      type: "string",
      minLength: 1,
      maxLength: 255,
      pattern: "^[A-Za-z0-9._:-]{1,255}$",
    })

    for (const { method, operation, path } of publicOperations(document)) {
      expect(
        operation.parameters,
        `${method.toUpperCase()} ${path} must reference RequestID`,
      ).toContainEqual({
        $ref: requestIDParameterRef,
      })
    }
  })

  it("documents only the implemented GET root discovery contract", () => {
    const rootPath = getRecord(document, "paths", "/")
    expect(Object.keys(rootPath)).toEqual(["get"])

    const operation = getRecord(rootPath, "get")
    expect(operation).toMatchObject({
      operationId: "getDiscovery",
      parameters: [
        {
          $ref: requestIDParameterRef,
        },
      ],
    })

    const responses = getRecord(operation, "responses")
    expect(Object.keys(responses)).toEqual(["200", "400", "405", "default"])
    expect(
      getRecord(responses, "200", "headers", "X-Request-ID"),
    ).toEqual({
      $ref: "#/components/headers/XRequestID",
    })
    expect(responses["400"]).toEqual({
      $ref: "#/components/responses/InvalidRequestIDProblem",
    })
    expect(responses["405"]).toEqual({
      $ref: "#/components/responses/MethodNotAllowedProblem",
    })
    expect(responses.default).toEqual({
      $ref: "#/components/responses/InternalProblem",
    })

    expect(
      getRecord(operation, "responses", "200", "content", "application/json"),
    ).toEqual({
      schema: {
        $ref: discoveryResponseRef,
      },
    })

    expect(
      getRecord(document, "components", "schemas", "DiscoveryResponse"),
    ).toEqual({
      type: "object",
      additionalProperties: false,
      required: ["service", "status", "api_version", "health"],
      properties: {
        service: {
          type: "string",
          const: "medpaperhub-api",
        },
        status: {
          type: "string",
          const: "ok",
        },
        api_version: {
          type: "string",
          const: "v1",
        },
        health: {
          type: "string",
          const: "/health",
        },
      },
    })
  })

  it("publishes the medpaperhub API health identity", () => {
    expect(
      getRecord(
        document,
        "components",
        "schemas",
        "HealthResponse",
        "properties",
        "service",
      ),
    ).toEqual({
      type: "string",
      const: "medpaperhub-api",
    })
  })

  it("reuses TaxonomySlug and TaxonomyItem without schema drift", () => {
    expect(
      getRecord(document, "components", "schemas", "TaxonomySlug"),
    ).toEqual({
      type: "string",
      minLength: 1,
      maxLength: 120,
      pattern: "^[a-z0-9]+(?:-[a-z0-9]+)*$",
    })

    const taxonomyItem = getRecord(
      document,
      "components",
      "schemas",
      "TaxonomyItem",
    )
    expect(getRecord(taxonomyItem, "properties", "slug")).toEqual({
      $ref: taxonomySlugRef,
    })

    for (const parameterName of ["Slug", "TopicFilter", "MethodFilter"]) {
      expect(
        getRecord(
          document,
          "components",
          "parameters",
          parameterName,
          "schema",
        ),
      ).toEqual({
        $ref: taxonomySlugRef,
      })
    }

    for (const schemaName of ["Topic", "Method"]) {
      expect(
        getRecord(document, "components", "schemas", schemaName),
      ).toEqual({
        $ref: taxonomyItemRef,
      })
    }
  })
})

function publicOperations(value: unknown) {
  const methods = new Set([
    "delete",
    "get",
    "head",
    "options",
    "patch",
    "post",
    "put",
    "trace",
  ])
  const operations: Array<{
    method: string
    operation: Record<string, unknown>
    path: string
  }> = []

  for (const [path, pathItem] of Object.entries(getRecord(value, "paths"))) {
    if (!isRecord(pathItem)) {
      continue
    }
    for (const [method, operation] of Object.entries(pathItem)) {
      if (methods.has(method) && isRecord(operation)) {
        operations.push({ method, operation, path })
      }
    }
  }

  return operations
}

function visitReferences(
  value: unknown,
  visitor: (reference: string) => void,
) {
  if (Array.isArray(value)) {
    for (const item of value) {
      visitReferences(item, visitor)
    }
    return
  }
  if (!isRecord(value)) {
    return
  }

  if (typeof value.$ref === "string") {
    visitor(value.$ref)
  }
  for (const item of Object.values(value)) {
    visitReferences(item, visitor)
  }
}

function resolveSchema(value: unknown, schema: unknown) {
  if (isRecord(schema) && typeof schema.$ref === "string") {
    return getRecord(resolveLocalReference(value, schema.$ref))
  }
  return getRecord(schema)
}

function resolveLocalReference(value: unknown, reference: string): unknown {
  if (!reference.startsWith("#/")) {
    throw new Error(`unsupported external reference: ${reference}`)
  }

  let current = value
  for (const encodedSegment of reference.slice(2).split("/")) {
    const segment = encodedSegment.replaceAll("~1", "/").replaceAll("~0", "~")
    const record = getRecord(current)
    if (!(segment in record)) {
      throw new Error(`unresolved reference: ${reference}`)
    }
    current = record[segment]
  }

  return current
}

function getRecord(
  value: unknown,
  ...path: string[]
): Record<string, unknown> {
  let current = value
  for (const segment of path) {
    if (!isRecord(current) || !(segment in current)) {
      throw new Error(`missing object path: ${path.join(".")}`)
    }
    current = current[segment]
  }
  if (!isRecord(current)) {
    throw new Error(`value is not an object: ${path.join(".")}`)
  }
  return current
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value)
}
