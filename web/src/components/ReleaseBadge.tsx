import { useEffect, useState } from "react";
import { REPO } from "./Chrome";

/** How long a release counts as "new". After this it is just the current
 *  version, and a badge that never goes away is a badge nobody reads. */
const NEW_FOR_DAYS = 14;

/** The site is prerendered, so the version cannot be baked in without going
 *  stale between deploys; it is read from the GitHub API in the browser. The
 *  answer is cached for the session so a click around the docs is one request,
 *  not one per page — the unauthenticated API allows 60 an hour per address. */
const CACHE_KEY = "raunen:latest-release";
const CACHE_TTL_MS = 6 * 60 * 60 * 1000;

type Release = { tag: string; url: string; published: string };

async function latestRelease(): Promise<Release | null> {
  try {
    const hit = sessionStorage.getItem(CACHE_KEY);
    if (hit) {
      const { at, release } = JSON.parse(hit) as { at: number; release: Release };
      if (Date.now() - at < CACHE_TTL_MS) return release;
    }
  } catch {
    // Private mode, or a cache entry from an older shape: refetch.
  }

  const res = await fetch(
    "https://api.github.com/repos/devjasha/raunen/releases/latest",
    { headers: { Accept: "application/vnd.github+json" } },
  );
  if (!res.ok) return null;
  const body = (await res.json()) as {
    tag_name?: string;
    html_url?: string;
    published_at?: string;
  };
  if (!body.tag_name || !body.published_at) return null;

  const release: Release = {
    tag: body.tag_name,
    url: body.html_url ?? `${REPO}/releases`,
    published: body.published_at,
  };
  try {
    sessionStorage.setItem(CACHE_KEY, JSON.stringify({ at: Date.now(), release }));
  } catch {
    // Storage is optional; the badge works without it.
  }
  return release;
}

/** A link to the newest release, shown only while that release is recent.
 *  Renders nothing before the fetch lands, or if it fails, so a rate-limited
 *  or offline visitor sees the header they would have seen anyway. */
export function ReleaseBadge() {
  const [release, setRelease] = useState<Release | null>(null);

  useEffect(() => {
    let live = true;
    latestRelease().then(
      (r) => {
        if (live) setRelease(r);
      },
      () => {},
    );
    return () => {
      live = false;
    };
  }, []);

  if (!release) return null;
  const age = Date.now() - Date.parse(release.published);
  if (!(age >= 0 && age < NEW_FOR_DAYS * 24 * 60 * 60 * 1000)) return null;

  return (
    <a
      className="release-badge"
      href={release.url}
      title={`raunen ${release.tag} was released — see the notes`}
    >
      <span className="release-dot" aria-hidden="true" />
      {release.tag}
      <span className="release-new">new</span>
    </a>
  );
}
