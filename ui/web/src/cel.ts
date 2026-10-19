// Converts react-querybuilder trees into CEL conditions for the payload engine.
//
// Written for this project; the behaviour (operator mapping, "key:value"
// values for map fields, string escaping, empty-group handling) is inspired by
// Bifrost's ui/lib/utils/celConverterRouting.ts (Copyright Maxim AI, Inc.,
// Apache License 2.0, https://github.com/maximhq/bifrost). No code was copied.
//
// This module only uses erasable TypeScript syntax so Node can import it
// directly in tests.

export interface QBRule {
  id?: string;
  field: string;
  operator: string;
  value?: unknown;
  valueSource?: string;
  disabled?: boolean;
}

export interface QBGroup {
  id?: string;
  combinator?: string;
  not?: boolean;
  disabled?: boolean;
  rules: Array<QBRule | QBGroup>;
}

/** Map fields whose builder value is "key:value". */
export const MAP_FIELDS = new Set(["headers", "params"]);
/** The free-form document field whose builder value is "path:value". */
export const BODY_FIELD = "body";

const STRING_METHODS: Record<string, string> = {
  contains: "contains",
  beginsWith: "startsWith",
  endsWith: "endsWith",
  matches: "matches",
};

const BINARY: Record<string, string> = {
  "=": "==",
  "!=": "!=",
  ">": ">",
  "<": "<",
  ">=": ">=",
  "<=": "<=",
};

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/;
const INDEX = /^(0|[1-9][0-9]*)$/;
// CEL reserved words cannot be used in field selections.
const RESERVED = new Set([
  "true", "false", "null", "in", "as", "break", "const", "continue", "else",
  "for", "function", "if", "import", "let", "loop", "package", "namespace",
  "return", "var", "void", "while",
]);

/** Quotes s as a CEL string literal. */
export function celString(s: string): string {
  let out = '"';
  for (const ch of s) {
    switch (ch) {
      case "\\": out += "\\\\"; break;
      case '"': out += '\\"'; break;
      case "\n": out += "\\n"; break;
      case "\r": out += "\\r"; break;
      case "\t": out += "\\t"; break;
      default: {
        const c = ch.codePointAt(0) ?? 0;
        out += c < 0x20 ? "\\u" + c.toString(16).padStart(4, "0") : ch;
      }
    }
  }
  return out + '"';
}

/** Splits "key:value" at the first colon. Without a colon the whole text is the key. */
export function splitKeyValue(v: unknown): { key: string; value: string } {
  const s = v == null ? "" : String(v);
  const i = s.indexOf(":");
  if (i < 0) return { key: s.trim(), value: "" };
  return { key: s.slice(0, i).trim(), value: s.slice(i + 1).trim() };
}

/** Splits a gjson-style dotted path, honouring backslash escapes. */
export function splitPath(path: string): string[] {
  const segs: string[] = [];
  let cur = "";
  for (let i = 0; i < path.length; i++) {
    const ch = path[i];
    if (ch === "\\" && i + 1 < path.length) {
      cur += path[++i];
    } else if (ch === ".") {
      segs.push(cur);
      cur = "";
    } else {
      cur += ch;
    }
  }
  segs.push(cur);
  return segs;
}

function accessor(seg: string): string {
  if (IDENT.test(seg) && !RESERVED.has(seg)) return "." + seg;
  if (INDEX.test(seg)) return "[" + seg + "]";
  return "[" + celString(seg) + "]";
}

/** Converts a body path like "metadata.client" to "body.metadata.client". */
export function bodyExpr(path: string): string | null {
  const segs = splitPath(path.trim());
  if (segs.some((s) => s === "")) return null;
  return BODY_FIELD + segs.map(accessor).join("");
}

/** Existence check for a body path. */
function bodyHas(path: string): string | null {
  const segs = splitPath(path.trim());
  if (segs.some((s) => s === "")) return null;
  const parent = BODY_FIELD + segs.slice(0, -1).map(accessor).join("");
  const last = segs[segs.length - 1];
  if (IDENT.test(last) && !RESERVED.has(last)) return `has(${parent}.${last})`;
  if (INDEX.test(last)) return `size(${parent}) > ${last}`;
  return `${celString(last)} in ${parent}`;
}

/**
 * Formats a body comparison value. JSON scalars (numbers, booleans, null,
 * quoted strings) are used as typed literals; anything else is a string.
 */
export function bodyLiteral(v: string): string {
  const t = v.trim();
  if (t === "true" || t === "false" || t === "null") return t;
  // The engine decodes integral JSON numbers as int and others as double;
  // CEL compares mixed numeric types by value.
  if (/^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/.test(t)) return t;
  if (t.length >= 2 && t.startsWith('"') && t.endsWith('"')) {
    try {
      const s = JSON.parse(t);
      if (typeof s === "string") return celString(s);
    } catch {
      // fall through
    }
  }
  return celString(v);
}

/** Parses an in/notIn value into list items (JSON array or comma separated). */
export function listItems(v: unknown): string[] {
  if (Array.isArray(v)) return v.map((x) => String(x));
  const s = v == null ? "" : String(v).trim();
  if (s.startsWith("[")) {
    try {
      const arr = JSON.parse(s);
      if (Array.isArray(arr)) return arr.map((x) => (typeof x === "string" ? x : JSON.stringify(x)));
    } catch {
      // fall through to comma splitting
    }
  }
  return s.split(",").map((x) => x.trim()).filter((x) => x !== "");
}

function compare(lhs: string, operator: string, rawValue: unknown, lit: (s: string) => string): string {
  if (operator === "in" || operator === "notIn") {
    const list = "[" + listItems(rawValue).map(lit).join(", ") + "]";
    return operator === "in" ? `${lhs} in ${list}` : `!(${lhs} in ${list})`;
  }
  const value = rawValue == null ? "" : String(rawValue);
  const method = STRING_METHODS[operator];
  if (method) {
    // String methods always take a string argument.
    return `${lhs}.${method}(${celString(value)})`;
  }
  const op = BINARY[operator];
  if (!op) return "";
  return `${lhs} ${op} ${lit(value)}`;
}

/** Converts one builder rule into a CEL expression ("" when incomplete). */
export function ruleToCEL(rule: QBRule): string {
  const { field, operator } = rule;
  if (!field || !operator || rule.disabled) return "";

  if (field === BODY_FIELD) {
    const { key, value } = splitKeyValue(rule.value);
    if (!key) return "";
    if (operator === "null" || operator === "notNull") {
      const has = bodyHas(key);
      if (!has) return "";
      return operator === "null" ? `!(${has})` : has;
    }
    const lhs = bodyExpr(key);
    if (!lhs) return "";
    return compare(lhs, operator, value, bodyLiteral);
  }

  if (MAP_FIELDS.has(field)) {
    const { key, value } = splitKeyValue(rule.value);
    if (!key) return "";
    const k = celString(key.toLowerCase());
    if (operator === "null") return `!(${k} in ${field})`;
    if (operator === "notNull") return `${k} in ${field}`;
    return compare(`${field}[${k}]`, operator, value, celString);
  }

  // Plain string variables are always defined; "not set" means empty.
  if (operator === "null") return `${field} == ""`;
  if (operator === "notNull") return `${field} != ""`;
  return compare(field, operator, rule.value, celString);
}

function isGroup(r: QBRule | QBGroup): r is QBGroup {
  return Array.isArray((r as QBGroup).rules);
}

/** Converts a (possibly nested) builder group into CEL ("" matches everything). */
export function groupToCEL(group: QBGroup | null | undefined): string {
  if (!group || group.disabled || !Array.isArray(group.rules)) return "";
  const parts: string[] = [];
  for (const r of group.rules) {
    if (isGroup(r)) {
      const inner = groupToCEL(r);
      if (inner) parts.push(r.not ? inner : `(${inner})`);
    } else {
      const e = ruleToCEL(r);
      if (e) parts.push(e);
    }
  }
  if (parts.length === 0) return "";
  const joined = parts.join(group.combinator === "or" ? " || " : " && ");
  if (group.not) return `!(${joined})`;
  return joined;
}

const RE2_UNSUPPORTED: Array<[string, string]> = [
  ["(?<=", "positive lookbehind"],
  ["(?<!", "negative lookbehind"],
  ["(?=", "positive lookahead"],
  ["(?!", "negative lookahead"],
];

/** Returns an error message when pattern is not a valid RE2 regular expression. */
export function checkRegex(pattern: string): string | null {
  if (!pattern) return null;
  for (const [tok, what] of RE2_UNSUPPORTED) {
    if (pattern.includes(tok)) return `RE2 does not support ${what} ${tok}...)`;
  }
  if (/\\[1-9]/.test(pattern)) return "RE2 does not support backreferences";
  try {
    new RegExp(pattern);
  } catch (e) {
    return "Invalid regular expression: " + (e instanceof Error ? e.message : String(e));
  }
  return null;
}

/** Collects regex problems in a builder tree. */
export function checkGroup(group: QBGroup | null | undefined): string[] {
  const out: string[] = [];
  const walk = (g: QBGroup) => {
    for (const r of g.rules) {
      if (isGroup(r)) {
        walk(r);
        continue;
      }
      if (r.operator !== "matches") continue;
      const v = MAP_FIELDS.has(r.field) || r.field === BODY_FIELD ? splitKeyValue(r.value).value : String(r.value ?? "");
      const err = checkRegex(v);
      if (err) out.push(`${r.field}: ${err}`);
    }
  };
  if (group && Array.isArray(group.rules)) walk(group);
  return out;
}
