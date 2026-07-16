import type { ProblemDetails } from "~/types/catalog"
import { CatalogApiError } from "~/utils/catalogApi"
import { CatalogQueryError } from "~/utils/catalogQuery"

export type CatalogResult<T> =
  | {
      data: T
      state: "ready"
    }
  | {
      problem: ProblemDetails
      state: "waiting"
    }
  | {
      problem: ProblemDetails
      state: "not-found"
    }
  | {
      problem?: ProblemDetails
      queryError?: {
        message: string
        parameter: string
      }
      state: "invalid"
    }
  | {
      error: {
        message: string
        name: string
      }
      problem?: ProblemDetails
      state: "error"
    }

export async function settleCatalogRequest<T>(
  request: Promise<T> | (() => Promise<T>),
): Promise<CatalogResult<T>> {
  try {
    return {
      data: await (typeof request === "function" ? request() : request),
      state: "ready",
    }
  } catch (error: unknown) {
    if (error instanceof CatalogQueryError) {
      return {
        queryError: {
          message: error.message,
          parameter: error.parameter,
        },
        state: "invalid",
      }
    }
    if (error instanceof CatalogApiError) {
      if (error.problem.code === "catalog_not_published") {
        return {
          problem: error.problem,
          state: "waiting",
        }
      }
      if (error.problem.code === "resource_not_found") {
        return {
          problem: error.problem,
          state: "not-found",
        }
      }
      if (
        error.problem.code === "validation_error"
        || error.problem.code === "invalid_cursor"
        || error.problem.code === "cursor_context_mismatch"
      ) {
        return {
          problem: error.problem,
          state: "invalid",
        }
      }
      return {
        error: serializeError(error),
        problem: error.problem,
        state: "error",
      }
    }
    return {
      error: serializeError(error),
      state: "error",
    }
  }
}

function serializeError(error: unknown) {
  if (error instanceof Error) {
    return {
      message: error.message,
      name: error.name,
    }
  }
  return {
    message: String(error),
    name: "Error",
  }
}
