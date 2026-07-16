import {
  createCatalogApiClient,
  resolveCatalogApiBase,
} from "~/utils/catalogApi"

export function useCatalogApi() {
  const config = useRuntimeConfig()
  const apiBaseUrl = resolveCatalogApiBase({
    internalApiBaseUrl: import.meta.server
      ? process.env.INTERNAL_API_BASE_URL
      : undefined,
    publicApiBaseUrl: config.public.apiBaseUrl,
    server: import.meta.server,
  })

  return createCatalogApiClient(apiBaseUrl)
}
