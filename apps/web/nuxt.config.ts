export default defineNuxtConfig({
  compatibilityDate: "2026-07-16",
  css: ["~/assets/css/main.css"],
  devtools: {
    enabled: false,
  },
  runtimeConfig: {
    public: {
      apiBaseUrl: "",
    },
  },
  app: {
    head: {
      title: "medpaperhub",
      titleTemplate: "%s · medpaperhub",
      htmlAttrs: {
        lang: "zh-CN",
      },
      meta: [
        {
          name: "viewport",
          content: "width=device-width, initial-scale=1",
        },
      ],
    },
  },
  typescript: {
    strict: true,
    typeCheck: true,
  },
})
