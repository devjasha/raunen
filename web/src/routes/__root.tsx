import {
  Outlet,
  createRootRoute,
  HeadContent,
  Scripts,
} from "@tanstack/react-router";
import type { ReactNode } from "react";
import appCss from "../styles/app.css?url";

export const Route = createRootRoute({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { title: "raunen — a terminal agent for local LLMs" },
      {
        name: "description",
        content:
          "A small terminal agent for local LLMs. One Go binary, no runtime, no server.",
      },
      // Social preview: a thumbnail and caption when the link is shared.
      { property: "og:type", content: "website" },
      { property: "og:title", content: "raunen — a terminal agent for local LLMs" },
      {
        property: "og:description",
        content:
          "A small terminal agent for local LLMs. One Go binary, no runtime, no server.",
      },
      { property: "og:image", content: "https://raunen.vercel.app/og-image.png" },
      { property: "og:image:width", content: "3168" },
      { property: "og:image:height", content: "2050" },
      {
        property: "og:image:alt",
        content: "raunen terminal showing a chat with a local LLM",
      },
      { name: "twitter:card", content: "summary_large_image" },
      { name: "twitter:title", content: "raunen — a terminal agent for local LLMs" },
      {
        name: "twitter:description",
        content:
          "A small terminal agent for local LLMs. One Go binary, no runtime, no server.",
      },
      { name: "twitter:image", content: "https://raunen.vercel.app/og-image.png" },
    ],
    links: [
      { rel: "stylesheet", href: appCss },
      {
        rel: "icon",
        href:
          "data:image/svg+xml,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 16 16'><text y='13' font-size='14'>🐉</text></svg>",
      },
    ],
  }),
  component: RootComponent,
});

function RootComponent() {
  return (
    <RootDocument>
      <Outlet />
    </RootDocument>
  );
}

function RootDocument({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body>
        {children}
        <Scripts />
      </body>
    </html>
  );
}
