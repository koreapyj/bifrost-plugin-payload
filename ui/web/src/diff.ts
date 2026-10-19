// Minimal line diff (LCS) for the dry-run panel. Erasable TypeScript only so
// Node can import it in tests.

export interface DiffLine {
  op: " " | "+" | "-";
  text: string;
}

const MAX_CELLS = 4_000_000;

/** Returns a line-by-line diff of a -> b. Very large inputs fall back to a whole replacement. */
export function lineDiff(a: string, b: string): DiffLine[] {
  const x = a.split("\n");
  const y = b.split("\n");
  // Trim the common prefix/suffix first; the LCS table only covers the middle.
  let start = 0;
  while (start < x.length && start < y.length && x[start] === y[start]) start++;
  let endX = x.length;
  let endY = y.length;
  while (endX > start && endY > start && x[endX - 1] === y[endY - 1]) {
    endX--;
    endY--;
  }
  const out: DiffLine[] = [];
  for (let i = 0; i < start; i++) out.push({ op: " ", text: x[i] });
  const mx = x.slice(start, endX);
  const my = y.slice(start, endY);
  if (mx.length * my.length > MAX_CELLS) {
    for (const t of mx) out.push({ op: "-", text: t });
    for (const t of my) out.push({ op: "+", text: t });
  } else {
    const n = mx.length;
    const m = my.length;
    const w = m + 1;
    const lcs = new Uint32Array((n + 1) * w);
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        lcs[i * w + j] = mx[i] === my[j] ? lcs[(i + 1) * w + j + 1] + 1 : Math.max(lcs[(i + 1) * w + j], lcs[i * w + j + 1]);
      }
    }
    let i = 0;
    let j = 0;
    while (i < n && j < m) {
      if (mx[i] === my[j]) {
        out.push({ op: " ", text: mx[i] });
        i++;
        j++;
      } else if (lcs[(i + 1) * w + j] >= lcs[i * w + j + 1]) {
        out.push({ op: "-", text: mx[i++] });
      } else {
        out.push({ op: "+", text: my[j++] });
      }
    }
    while (i < n) out.push({ op: "-", text: mx[i++] });
    while (j < m) out.push({ op: "+", text: my[j++] });
  }
  for (let i = endX; i < x.length; i++) out.push({ op: " ", text: x[i] });
  return out;
}

/** Pretty-prints JSON text; returns the input unchanged when it does not parse. */
export function pretty(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}
