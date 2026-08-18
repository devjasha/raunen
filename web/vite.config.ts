import { defineConfig } from "vite";
import viteReact from "@vitejs/plugin-react";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";

// Deployed to the root of a domain (Vercel), so links and assets are absolute
// from "/".
const base = "/";

export default defineConfig({
  base,
  plugins: [
    tanstackStart({
      // Static output: every route is rendered to HTML at build time, so the
      // site is served with no server and still read by crawlers that do not
      // run JavaScript.
      prerender: { enabled: true, crawlLinks: true },
      spa: { enabled: false },
      pages: [{ path: "/" }],
    }),
    viteReact(),
  ],
});
