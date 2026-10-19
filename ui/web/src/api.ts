import type { Config } from "./types.ts";

const PLUGIN_URL = "/api/plugins/payload";
const DRAFT_KEY = "payload-ui:draft";

export interface PluginMeta {
  enabled: boolean;
  path: string | null;
  placement?: string | null;
  order?: number | null;
}

export interface LoadResult {
  meta: PluginMeta;
  config: unknown;
  raw: Record<string, unknown>;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

/** Thrown when the store cannot be written (Bifrost runs without a configstore). */
export class ConfigStoreDisabledError extends ApiError {}

async function errorMessage(resp: Response): Promise<string> {
  const text = await resp.text().catch(() => "");
  try {
    const j = JSON.parse(text);
    const m = j?.error?.message ?? j?.message;
    if (typeof m === "string" && m) return m;
  } catch {
    // not JSON
  }
  return text.trim() || `HTTP ${resp.status}`;
}

export interface Draft {
  config: Config;
  savedAt: string;
}

export function stashDraft(config: Config): void {
  try {
    sessionStorage.setItem(DRAFT_KEY, JSON.stringify({ config, savedAt: new Date().toISOString() } satisfies Draft));
  } catch {
    // storage unavailable; nothing else we can do
  }
}

export function readDraft(): Draft | null {
  try {
    const t = sessionStorage.getItem(DRAFT_KEY);
    if (!t) return null;
    const d = JSON.parse(t) as Draft;
    return d && typeof d === "object" && d.config ? d : null;
  } catch {
    return null;
  }
}

export function clearDraft(): void {
  try {
    sessionStorage.removeItem(DRAFT_KEY);
  } catch {
    // ignore
  }
}

/** Sends the browser to the Bifrost dashboard login, stashing the draft first. */
export function redirectToLogin(draft: Config | null): never {
  if (draft) stashDraft(draft);
  location.href = "/login";
  throw new ApiError(401, "Session expired; redirecting to the login page");
}

export async function loadPlugin(): Promise<LoadResult> {
  const resp = await fetch(PLUGIN_URL, { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } });
  if (resp.status === 401) redirectToLogin(null);
  if (!resp.ok) throw new ApiError(resp.status, await errorMessage(resp));
  const raw = (await resp.json()) as Record<string, unknown>;
  return {
    raw,
    config: raw.config,
    meta: {
      enabled: raw.enabled !== false,
      path: (raw.path as string | null) ?? null,
      placement: (raw.placement as string | null | undefined) ?? undefined,
      order: (raw.order as number | null | undefined) ?? undefined,
    },
  };
}

/**
 * Saves config, keeping enabled/path/placement/order unchanged. Bifrost merges
 * top-level config keys over the stored ones, so the full config is always sent.
 */
export async function savePlugin(meta: PluginMeta, config: Config): Promise<void> {
  const body: Record<string, unknown> = { enabled: meta.enabled, path: meta.path, config };
  if (meta.placement != null) body.placement = meta.placement;
  if (meta.order != null) body.order = meta.order;
  const resp = await fetch(PLUGIN_URL, {
    method: "PUT",
    credentials: "same-origin",
    cache: "no-store",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
  });
  if (resp.status === 401) redirectToLogin(config);
  if (resp.ok) return;
  const msg = await errorMessage(resp);
  if (resp.status === 400 && /configstore is disabled/i.test(msg)) throw new ConfigStoreDisabledError(400, msg);
  throw new ApiError(resp.status, msg);
}
