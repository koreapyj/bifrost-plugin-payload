import type { Config, FieldError, MatchContext } from "./types.ts";

export interface ValidateResult {
  ok: boolean;
  errors: FieldError[];
}

export interface ApplyResult {
  ok: boolean;
  body: string;
  changed: boolean;
  applied: string[];
  errors: FieldError[];
}

interface RawEngine {
  validate(config: string): string;
  apply(config: string, doc: string, ctx: string): string;
  ready?: boolean;
}

declare global {
  // Provided by wasm_exec.js (Go distribution) and by ui/wasm/main.go.
  // eslint-disable-next-line no-var
  var Go: new () => { importObject: WebAssembly.Imports; run(i: WebAssembly.Instance): Promise<void> };
  // eslint-disable-next-line no-var
  var payloadEngine: RawEngine | undefined;
}

export class Engine {
  constructor(private raw: RawEngine) {}

  validate(cfg: Config | string): ValidateResult {
    const text = typeof cfg === "string" ? cfg : JSON.stringify(cfg);
    return JSON.parse(this.raw.validate(text)) as ValidateResult;
  }

  apply(cfg: Config, doc: string, ctx: MatchContext): ApplyResult {
    return JSON.parse(this.raw.apply(JSON.stringify(cfg), doc, JSON.stringify(ctx))) as ApplyResult;
  }
}

async function instantiate(url: string, imports: WebAssembly.Imports): Promise<WebAssembly.Instance> {
  const resp = await fetch(url, { credentials: "same-origin", cache: "no-store" });
  if (!resp.ok) throw new Error(`engine.wasm: HTTP ${resp.status}`);
  if (WebAssembly.instantiateStreaming && (resp.headers.get("content-type") || "").startsWith("application/wasm")) {
    try {
      return (await WebAssembly.instantiateStreaming(resp.clone(), imports)).instance;
    } catch {
      // fall back to the buffered path below
    }
  }
  return (await WebAssembly.instantiate(await resp.arrayBuffer(), imports)).instance;
}

/** Loads engine.wasm relative to the page base URL and resolves when the engine is installed. */
export async function loadEngine(): Promise<Engine> {
  if (typeof globalThis.Go !== "function") throw new Error("wasm_exec.js is not loaded");
  const go = new globalThis.Go();
  const url = new URL("engine.wasm", document.baseURI).toString();
  const instance = await instantiate(url, go.importObject);
  void go.run(instance);
  // main() installs payloadEngine synchronously before blocking.
  for (let i = 0; i < 100 && !globalThis.payloadEngine?.ready; i++) {
    await new Promise((r) => setTimeout(r, 10));
  }
  if (!globalThis.payloadEngine?.ready) throw new Error("engine did not start");
  return new Engine(globalThis.payloadEngine);
}
