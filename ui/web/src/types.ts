import type { QBGroup } from "./cel.ts";

export const STAGES = ["http", "request", "llm"] as const;
export const SCOPES = ["virtual_key", "user", "team", "customer", "global"] as const;

export interface Actions {
  default?: Record<string, unknown>;
  override?: Record<string, unknown>;
  filter?: string[];
}

/** Mirrors payload.Rule (snake_case JSON). */
export interface Rule {
  name: string;
  description?: string;
  enabled?: boolean;
  stage: string;
  scope?: string;
  scope_id: string | null;
  priority: number;
  chain_rule: boolean;
  cel_expression: string;
  query: QBGroup | null;
  actions: Actions;
}

export interface UIConfig {
  enabled: boolean;
  path?: string;
  api_base?: string;
}

/** Mirrors payload.Config. */
export interface Config {
  rules: Rule[];
  passthrough_extra_params: boolean;
  ui?: UIConfig;
  [extra: string]: unknown;
}

export interface FieldError {
  path: string;
  message: string;
}

/** Mirrors payload.MatchContext. */
export interface MatchContext {
  stage: string;
  provider: string;
  model: string;
  request_type: string;
  path: string;
  from_protocol: string;
  body_format: string;
  headers: Record<string, string>;
  params: Record<string, string>;
  virtual_key_id: string;
  virtual_key_name: string;
  user_id: string;
  team_id: string;
  team_name: string;
  customer_id: string;
  customer_name: string;
}

/** A rule plus a client-only key used for React identity; the key is never serialized. */
export interface RuleItem {
  key: string;
  rule: Rule;
}

let seq = 0;
export function newKey(): string {
  seq += 1;
  return "r" + seq;
}

export function newRule(stage = "http"): Rule {
  return {
    name: "new-rule",
    enabled: true,
    stage,
    scope: "global",
    scope_id: null,
    priority: 0,
    chain_rule: false,
    cel_expression: "",
    query: null,
    actions: {},
  };
}

/** Normalizes whatever the API returned into a full Config. */
export function normalizeConfig(raw: unknown): Config {
  const c = (raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {}) as Record<string, unknown>;
  const rules = Array.isArray(c.rules) ? (c.rules as Rule[]) : [];
  return {
    ...c,
    rules: rules.map((r) => ({
      ...r,
      enabled: r.enabled === undefined || r.enabled === null ? true : r.enabled,
      scope: r.scope || "global",
      scope_id: r.scope_id ?? null,
      priority: typeof r.priority === "number" ? r.priority : Number(r.priority) || 0,
      chain_rule: !!r.chain_rule,
      cel_expression: r.cel_expression ?? "",
      query: r.query ?? null,
      actions: r.actions ?? {},
    })),
    passthrough_extra_params: !!c.passthrough_extra_params,
    // Keep "ui" only when present: Bifrost merges top-level keys on save, so an
    // absent key leaves the stored UI settings untouched.
    ...(c.ui && typeof c.ui === "object" ? { ui: c.ui as UIConfig } : {}),
  };
}

/** Removes empty optional members so the stored JSON stays tidy. */
export function cleanRule(r: Rule): Rule {
  const out: Rule = { ...r, actions: { ...r.actions } };
  if (!out.description) delete out.description;
  if (out.scope === "global") out.scope_id = null;
  const a = out.actions;
  if (a.default && Object.keys(a.default).length === 0) delete a.default;
  if (a.override && Object.keys(a.override).length === 0) delete a.override;
  if (a.filter && a.filter.length === 0) delete a.filter;
  return out;
}

export function toConfig(base: Config, items: RuleItem[]): Config {
  return { ...base, rules: items.map((i) => cleanRule(i.rule)) };
}
