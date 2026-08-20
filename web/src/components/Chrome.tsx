import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { ReleaseBadge } from "./ReleaseBadge";

/** The install command, in one place: it appears on the home page and in the
 *  docs, and two copies that drift is how a wrong command reaches a user. */
export const INSTALL_CMD =
  "curl -fsSL https://raw.githubusercontent.com/devjasha/raunen/main/install.sh | sh";

export const REPO = "https://github.com/devjasha/raunen";

export function Header() {
  return (
    <header>
      <div className="wrap">
        <Link to="/" className="brand">
          raunen
        </Link>
        <nav>
          <ReleaseBadge />
          <ThemeToggle />
          <Link to="/docs">Docs</Link>
          <Link to="/cost">Cost</Link>
          <a className="opt" href={REPO}>
            GitHub
          </a>
        </nav>
      </div>
    </header>
  );
}

/** A light/dark toggle. The initial theme is whatever the head script already
 *  applied (or the OS default); the toggle writes `data-theme` on <html> and
 *  mirrors it into localStorage so the choice sticks and survives a reload
 *  without a flash. Removing the attribute returns to following the OS. */
export function ThemeToggle() {
  const [theme, setTheme] = useState<"light" | "dark">("light");

  useEffect(() => {
    const current = document.documentElement.getAttribute("data-theme");
    if (current === "dark" || current === "light") setTheme(current);
    else {
      const dark =
        window.matchMedia &&
        window.matchMedia("(prefers-color-scheme: dark)").matches;
      setTheme(dark ? "dark" : "light");
    }
  }, []);

  const toggle = () => {
    const next = theme === "dark" ? "light" : "dark";
    setTheme(next);
    document.documentElement.setAttribute("data-theme", next);
    try {
      localStorage.setItem("raunen:theme", next);
    } catch {
      // Storage is optional; the toggle still works for the session.
    }
  };

  return (
    <button
      type="button"
      className="theme-toggle"
      onClick={toggle}
      aria-label={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
      title={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
    >
      {theme === "dark" ? "☀" : "☾"}
    </button>
  );
}

export function Footer() {
  return (
    <footer>
      <div className="wrap">
        <span>MIT licensed · no telemetry · your code stays on your machine</span>
        <span>
          <a href={REPO}>source</a> · <Link to="/docs">docs</Link> ·{" "}
          <a href={`${REPO}/releases`}>releases</a>
        </span>
      </div>
    </footer>
  );
}

/** A terminal frame. `title` is the window label, not part of the transcript. */
export function Term({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="term">
      <div className="term-bar">
        <span className="dot" />
        <span className="dot" />
        <span className="dot" />
        <span className="title">{title}</span>
      </div>
      <pre>{children}</pre>
    </div>
  );
}

/* Transcript colours, named for what they mean rather than for the colour, so
   a change of palette does not turn into a change of markup. */
export const U = ({ children }: { children: ReactNode }) => (
  <span className="u">{children}</span>
);
export const T = ({ children }: { children: ReactNode }) => (
  <span className="t">{children}</span>
);
export const D = ({ children }: { children: ReactNode }) => (
  <span className="d">{children}</span>
);
export const G = ({ children }: { children: ReactNode }) => (
  <span className="g">{children}</span>
);

/** Copy-to-clipboard for a command. Falls back to selecting the text, which is
 *  what happens on an insecure origin where the clipboard API is refused. */
export function CopyLine({ cmd }: { cmd: string }) {
  return (
    <div className="install">
      <code id="install-cmd">{cmd}</code>
      <button
        type="button"
        className="copy"
        onClick={(e) => {
          const btn = e.currentTarget;
          navigator.clipboard?.writeText(cmd).then(
            () => {
              btn.textContent = "copied";
              setTimeout(() => (btn.textContent = "copy"), 1200);
            },
            () => {
              const node = document.getElementById("install-cmd");
              if (!node) return;
              const r = document.createRange();
              r.selectNodeContents(node);
              const s = getSelection();
              s?.removeAllRanges();
              s?.addRange(r);
            },
          );
        }}
      >
        copy
      </button>
    </div>
  );
}
