import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { Header, Footer } from "../components/Chrome";

export const Route = createFileRoute("/docs")({
  component: DocsLayout,
});

/** One list, used to render the sidebar. Adding a page means adding a route
 *  file and one entry here — the two are not derived from each other, so a
 *  page that is not listed is unreachable rather than silently orphaned. */
const NAV = [
  {
    title: "Start here",
    items: [
      { to: "/docs", label: "Overview", exact: true },
      { to: "/docs/install", label: "Install" },
      { to: "/docs/configuration", label: "Configuration" },
    ],
  },
  {
    title: "Using it",
    items: [
      { to: "/docs/modes", label: "Modes" },
      { to: "/docs/commands", label: "Commands & keys" },
      { to: "/docs/models", label: "Models & ladders" },
      { to: "/docs/subagents", label: "Sub-agents" },
    ],
  },
  {
    title: "Reference",
    items: [
      { to: "/docs/mcp", label: "MCP servers" },
      { to: "/docs/local-models", label: "Local models" },
    ],
  },
] as const;

function DocsLayout() {
  return (
    <>
      <Header />
      <div className="wrap">
        <div className="docs">
          <aside className="sidebar">
            {NAV.map((group) => (
              <div key={group.title}>
                <h4>{group.title}</h4>
                <ul>
                  {group.items.map((item) => (
                    <li key={item.to}>
                      <Link
                        to={item.to}
                        activeOptions={{ exact: "exact" in item }}
                        activeProps={{ className: "active" }}
                      >
                        {item.label}
                      </Link>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
          </aside>
          <div className="prose">
            <Outlet />
          </div>
        </div>
      </div>
      <Footer />
    </>
  );
}
