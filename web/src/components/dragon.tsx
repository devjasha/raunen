import type { CSSProperties } from "react";

/** The ASCII dragon, byte-for-byte as it appears in the README and on the
 *  companion screen. A template literal because the art is full of
 *  backslashes; it is escaped here and nowhere else, so the rendered output
 *  matches the terminal. */
export const DRAGON = `                \\||/
                |  @___oo
      /\\  /\\   / (__,,,,|
     ) /^\\) ^\\/ _)
     )   /^\\/   _)
     )   _ /  / _)
 /\\  )/\\/ ||  | )_)
<  >      |(,,) )__)
 ||      /    \\)___)\\
 | \\____(      )___) )___
  \\______(_______;;; __;;;`;

/** A Shenlong-style pixel dragon, drawn as a grid of square pixels so it keeps
 *  a crisp, blocky look at any size. Colours are CSS classes (see app.css) so
 *  the same art reads on a light or dark page.
 *
 *  Legend:  . empty   G body    L highlight   Y belly   O outline
 *           R eye      W white (horn / whisker / pearl)   P pearl core
 */
const GRID = [
  "...W..W......................",
  "...W..W......................",
  "...W..W......................",
  "..LWLWLLL.....................",
  ".GGGGGGGG.....................",
  ".GGRGGGGG.....................",
  "WGGGGGGGG.....................",
  "GGGGGGGGGL....................",
  ".YYGGGGGGGGLLLGGGL.....LGGLWW.",
  ".GGGGGGGG..GGG...GLLLLGG..WPPW",
  "..................GGGGL....WW.",
  "...................GGG........",
  "..............................",
  "..............................",
];

const FILL: Record<string, string> = {
  G: "px-g",
  L: "px-l",
  Y: "px-y",
  O: "px-o",
  R: "px-r",
  W: "px-w",
  P: "px-p",
};

// Pixels that make up the glowing pearl, so they can carry an extra shadow.
const PEARL = new Set(["26,9", "27,9", "28,9", "29,9", "27,8", "28,8", "27,10", "28,10"]);

const P = 8; // pixel size
const X0 = 24; // left padding, so the whiskers have room to extend
const W = X0 + GRID[0].length * P;
const H = GRID.length * P;

export function DragonArt({ className }: { className?: string }) {
  const pixels = [];
  for (let r = 0; r < GRID.length; r++) {
    const row = GRID[r];
    for (let c = 0; c < row.length; c++) {
      const ch = row[c];
      if (ch === "." || !FILL[ch]) continue;
      const cls = [FILL[ch], PEARL.has(`${c},${r}`) && "px-glow"]
        .filter(Boolean)
        .join(" ");
      pixels.push(
        <rect
          key={`${c},${r}`}
          x={X0 + c * P}
          y={r * P}
          width={P}
          height={P}
          className={cls}
        />,
      );
    }
  }

  const whisker: CSSProperties = { strokeWidth: 6, strokeLinecap: "square" };

  return (
    <svg
      className={className}
      viewBox={`0 0 ${W} ${H}`}
      role="img"
      aria-label="raunen dragon"
      shapeRendering="crispEdges"
      xmlns="http://www.w3.org/2000/svg"
    >
      <g className="px-w" style={whisker}>
        {/* whiskers trailing from the snout */}
        <line x1={X0} y1={52} x2={4} y2={42} />
        <line x1={X0} y1={60} x2={4} y2={68} />
      </g>
      {pixels}
    </svg>
  );
}
