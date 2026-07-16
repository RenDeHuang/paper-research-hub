export default defineEventHandler((event) => {
  setResponseStatus(event, 405, "Method Not Allowed")
  setHeader(event, "Content-Type", "application/problem+json")
  setHeader(event, "Allow", "GET")

  return {
    type: "urn:paper-hub:problem:method-not-allowed",
    title: "Method Not Allowed",
    status: 405,
    detail: "Only GET is supported for /health.",
    instance: "/health",
  }
})
