import { defineConfig } from "vite";
import viteReact from "@vitejs/plugin-react";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";

// Served from a subpath on GitHub Pages (devjasha.github.io/raunen/) unless a
// custom domain is set, in which case it is the root. Absolute links break
// under the wrong one, so it comes from the environment and the workflow sets
// it — rather than being hardcoded to whichever we happened to test.
const base = process.env.SITE_BASE ?? "/";

export default defineConfig({
  base,
  plugins: [
    tanstackStart({
      // Static output: every route is rendered to HTML at build time, so the
      // site can be served from GitHub Pages with no server and still be read
      // by crawlers that do not run JavaScript.
      prerender: { enabled: true, crawlLinks: true },
      spa: { enabled: false },
      pages: [{ path: "/" }],
    }),
    viteReact(),
  ],
});
