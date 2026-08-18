import { defineConfig } from "vite-plus";

export default defineConfig({
  fmt: {
    ignorePatterns: ["ephemeral/**", "README.md", "docs/**", "graph/**", "harness/**"],
    singleQuote: false,
  },
  lint: {
    jsPlugins: [{ name: "vite-plus", specifier: "vite-plus/oxlint-plugin" }],
    rules: { "vite-plus/prefer-vite-plus-imports": "error" },
    // rsvelte-check owns type-checking because it generates the Svelte overlays
    // that ts-go needs for named exports from .svelte modules.
    options: { typeAware: true, typeCheck: false },
  },
  staged: {
    "*.{astro,css,js,json,md,mjs,ts,yaml,yml}": "vp check --fix",
    "*.svelte": "vp run format:rsvelte && vp run lint:rsvelte && vp run check:rsvelte",
  },
});
