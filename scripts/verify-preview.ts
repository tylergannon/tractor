import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";

import { preview } from "astro";

const base = "/tractor";
const server = await preview({
  logLevel: "silent",
  root: fileURLToPath(new URL("../", import.meta.url)),
  server: { host: "127.0.0.1", port: 0 },
});

try {
  const origin = `http://${server.host ?? "127.0.0.1"}:${server.port}`;
  const expectedPages = [
    {
      path: `${base}/`,
      text: ["Put your agents", "Prompt your agent", "Author a pipeline"],
      island: true,
    },
    {
      path: `${base}/docs/authoring-pipelines/`,
      text: ["Your first pipeline", "Choose the right node", "Fan out, then converge"],
    },
    {
      path: `${base}/docs/tractor-vs-attractor/`,
      text: ["The short version", "Typed data replaces DOT", "strongdm/attractor"],
    },
  ];

  let homeHtml = "";
  for (const page of expectedPages) {
    const response = await fetch(new URL(page.path, origin));
    assert.equal(response.status, 200, `${page.path} did not return HTTP 200`);
    const html = await response.text();
    for (const expected of page.text) {
      assert.ok(html.includes(expected), `${page.path} is missing ${expected}`);
    }
    if (page.island) {
      assert.ok(html.includes("<astro-island uid="), "Home page is missing the Svelte island");
      assert.ok(html.includes('client="load"'), "Install prompt is not hydrated on load");
      assert.ok(html.includes('data-testid="install-prompt"'), "Install prompt marker is missing");
      homeHtml = html;
    }
  }

  assert.ok(
    homeHtml.includes(`href="${base}/docs/authoring-pipelines/"`),
    "Home page is missing the base-prefixed authoring link",
  );
  assert.ok(!homeHtml.includes(`${base}docs/`), "Home page contains a malformed base URL");

  const entryPaths = [
    ...homeHtml.matchAll(/(?:component-url|renderer-url)="([^"?]+\.js)/g),
    ...homeHtml.matchAll(/href="([^"?]+\.css)/g),
  ].map((match) => match[1]);
  assert.ok(entryPaths.length >= 3, "Expected compiled Svelte and CSS entrypoints");

  const assetPaths = new Set(entryPaths);
  const pending = [...assetPaths];
  const importPatterns = [
    /(?:import|export)\s*(?:[^"'()]*?\bfrom\s*)?["']([^"']+)["']/g,
    /\bimport\s*\(\s*["']([^"']+)["']/g,
  ];

  for (const assetPath of pending) {
    assert.ok(
      assetPath.startsWith(`${base}/_astro/`),
      `Asset escaped GitHub Pages base: ${assetPath}`,
    );
    const response = await fetch(new URL(assetPath, origin));
    assert.equal(response.status, 200, `${assetPath} did not return HTTP 200`);
    const bytes = await response.arrayBuffer();
    assert.ok(bytes.byteLength > 0, `${assetPath} was empty`);

    if (!assetPath.endsWith(".js")) continue;
    const source = new TextDecoder().decode(bytes);
    for (const pattern of importPatterns) {
      for (const match of source.matchAll(pattern)) {
        const imported = new URL(match[1], new URL(assetPath, origin));
        if (imported.origin !== origin || !imported.pathname.startsWith(`${base}/_astro/`))
          continue;
        if (!assetPaths.has(imported.pathname)) {
          assetPaths.add(imported.pathname);
          pending.push(imported.pathname);
        }
      }
    }
  }

  console.log(
    `Preview verified ${expectedPages.length} routes and ${assetPaths.size} GitHub Pages-safe assets.`,
  );
} finally {
  await server.stop();
}
