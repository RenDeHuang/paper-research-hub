<template>
  <main aria-labelledby="replatform-heading">
    <p class="eyebrow">
      Paper Research Hub
    </p>
    <h1 id="replatform-heading">
      Go + Nuxt 重构中
    </h1>
    <p>新的论文研究门户正在建立最小运行基线。</p>
  </main>
</template>

<style>
:root {
  color: #172033;
  background: #f4f7fb;
  font-family:
    Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI",
    sans-serif;
}

body {
  margin: 0;
}

main {
  box-sizing: border-box;
  display: grid;
  min-height: 100vh;
  place-content: center;
  padding: 2rem;
  text-align: center;
}

.eyebrow {
  margin: 0;
  color: #4169a1;
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.14em;
  text-transform: uppercase;
}

h1 {
  margin: 0.75rem 0;
  font-size: clamp(2rem, 8vw, 4.5rem);
  line-height: 1.05;
}

main > p:last-child {
  margin: 0;
  color: #526078;
  font-size: 1.05rem;
}
</style>
