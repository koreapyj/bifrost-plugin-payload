import { useEffect, useMemo, useState } from "react";
import { lineDiff, pretty } from "./diff.ts";
import type { ApplyResult, Engine } from "./engine.ts";
import { STAGES, type Config, type FieldError, type MatchContext } from "./types.ts";

export function ErrorList({ errors }: { errors: FieldError[] }) {
  if (errors.length === 0) return null;
  return (
    <ul className="error-list">
      {errors.map((e, i) => (
        <li key={i}>
          {e.path && <code>{e.path}</code>} {e.message}
        </li>
      ))}
    </ul>
  );
}

async function copy(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [state, setState] = useState("");
  return (
    <button
      type="button"
      onClick={async () => {
        setState((await copy(text)) ? "Copied" : "Copy failed: select the text manually");
        setTimeout(() => setState(""), 2000);
      }}
    >
      {state || label}
    </button>
  );
}

/** Whole-config JSON editor. Changes are applied explicitly. */
export function JsonTab(props: { config: Config; engine: Engine | null; onApply: (c: unknown) => void }) {
  const current = useMemo(() => JSON.stringify(props.config, null, 2), [props.config]);
  const [text, setText] = useState(current);
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (!dirty) setText(current);
  }, [current, dirty]);

  let parseError = "";
  let parsed: unknown = undefined;
  try {
    parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) parseError = "The config must be a JSON object.";
  } catch (e) {
    parseError = e instanceof Error ? e.message : String(e);
  }
  const validation = !parseError && props.engine ? props.engine.validate(text) : null;

  return (
    <div className="panel">
      <p className="muted small">
        The complete plugin config, including <code>passthrough_extra_params</code> and the <code>ui</code> section. Apply to load it
        into the editor; nothing is stored until you save.
      </p>
      <textarea
        className="code big"
        spellCheck={false}
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          setDirty(true);
        }}
      />
      <div className="row">
        <button type="button" className="primary" disabled={!!parseError || !dirty} onClick={() => (props.onApply(parsed), setDirty(false))}>
          Apply to editor
        </button>
        <button type="button" disabled={!!parseError} onClick={() => setText(pretty(text))}>
          Format
        </button>
        <button type="button" disabled={!dirty} onClick={() => (setText(current), setDirty(false))}>
          Revert
        </button>
        <CopyButton text={text} />
      </div>
      {parseError && <p className="err">JSON: {parseError}</p>}
      {validation && (validation.ok ? <p className="ok-text">Valid configuration.</p> : <ErrorList errors={validation.errors} />)}
    </div>
  );
}

/** Read-only display used when Bifrost has no configstore (config.json deployments). */
export function CopyMode(props: { config: Config; message: string; onBack: () => void }) {
  const text = JSON.stringify(props.config, null, 2);
  return (
    <div className="panel">
      <p className="notice warn">
        {props.message}. Bifrost cannot store plugin configuration at runtime. Copy this JSON into the payload plugin's{" "}
        <code>config</code> in <code>config.json</code> and restart Bifrost. Bifrost does not return the file-based config here, so
        if you started from an empty editor, paste your current config into the Config JSON tab first.
      </p>
      <textarea className="code big" readOnly value={text} />
      <div className="row">
        <CopyButton text={text} label="Copy JSON for config.json" />
        <button type="button" onClick={props.onBack}>
          Back to editor
        </button>
      </div>
    </div>
  );
}

const SAMPLE_BODY = JSON.stringify(
  { model: "openai/gpt-4o", messages: [{ role: "user", content: "Hello" }], metadata: { debug: true } },
  null,
  2,
);

function parseLines(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf(":");
    if (i <= 0) continue;
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

type CtxText = Omit<MatchContext, "headers" | "params"> & { headers: string; params: string };

const CTX_FIELDS: Array<[keyof CtxText, string, string]> = [
  ["provider", "Provider", "openai"],
  ["model", "Model", "gpt-4o"],
  ["request_type", "Request type", "chat_completion"],
  ["path", "Path", "/v1/chat/completions"],
  ["from_protocol", "From protocol", "openai"],
  ["virtual_key_id", "Virtual key ID", ""],
  ["virtual_key_name", "Virtual key name", ""],
  ["user_id", "User ID", ""],
  ["team_id", "Team ID", ""],
  ["team_name", "Team name", ""],
  ["customer_id", "Customer ID", ""],
  ["customer_name", "Customer name", ""],
];

const DRYRUN_KEY = "payload-ui:dryrun";

function loadDryRun(): { body: string; ctx: CtxText } {
  const ctx: CtxText = {
    stage: "http",
    provider: "openai",
    model: "gpt-4o",
    request_type: "",
    path: "/v1/chat/completions",
    from_protocol: "openai",
    body_format: "raw",
    headers: "x-tier: pro",
    params: "",
    virtual_key_id: "",
    virtual_key_name: "",
    user_id: "",
    team_id: "",
    team_name: "",
    customer_id: "",
    customer_name: "",
  };
  try {
    const saved = JSON.parse(localStorage.getItem(DRYRUN_KEY) || "null");
    if (saved && typeof saved.body === "string") return { body: saved.body, ctx: { ...ctx, ...saved.ctx } };
  } catch {
    // ignore
  }
  return { body: SAMPLE_BODY, ctx };
}

export function DryRun(props: { config: Config; engine: Engine | null }) {
  const [initial] = useState(loadDryRun);
  const [body, setBody] = useState(initial.body);
  const [ctx, setCtx] = useState<CtxText>(initial.ctx);
  const [result, setResult] = useState<ApplyResult | null>(null);

  useEffect(() => {
    try {
      localStorage.setItem(DRYRUN_KEY, JSON.stringify({ body, ctx }));
    } catch {
      // ignore
    }
  }, [body, ctx]);

  useEffect(() => {
    if (!props.engine) return;
    const t = setTimeout(() => {
      const mc: MatchContext = { ...ctx, headers: parseLines(ctx.headers), params: parseLines(ctx.params) };
      setResult(props.engine!.apply(props.config, body, mc));
    }, 200);
    return () => clearTimeout(t);
  }, [props.config, props.engine, body, ctx]);

  const set = (k: keyof CtxText, v: string) => setCtx({ ...ctx, [k]: v });
  const before = pretty(body);
  const after = result?.ok ? pretty(result.body) : "";
  const diff = result?.ok && result.changed ? lineDiff(before, after) : [];

  return (
    <div className="panel dryrun">
      <p className="muted small">
        Runs the current (unsaved) rules in the browser with the same engine as the server. For the request/llm stages with{" "}
        <code>body_format = bifrost</code>, paste the Bifrost view JSON (internal request with <code>params</code>) as the body.
      </p>
      <div className="dry-grid">
        <div>
          <h4>Sample body</h4>
          <textarea className="code big" spellCheck={false} value={body} onChange={(e) => setBody(e.target.value)} />
        </div>
        <div>
          <h4>Match context</h4>
          <div className="grid2">
            <label>
              Stage
              <select value={ctx.stage} onChange={(e) => set("stage", e.target.value)}>
                {STAGES.map((s) => (
                  <option key={s}>{s}</option>
                ))}
              </select>
            </label>
            <label>
              Body format
              <select value={ctx.body_format} onChange={(e) => set("body_format", e.target.value)}>
                <option>raw</option>
                <option>bifrost</option>
              </select>
            </label>
            {CTX_FIELDS.map(([k, label, ph]) => (
              <label key={k}>
                {label}
                <input value={ctx[k] as string} placeholder={ph} onChange={(e) => set(k, e.target.value)} />
              </label>
            ))}
          </div>
          <label>
            Headers (one "Name: value" per line)
            <textarea className="code" rows={3} value={ctx.headers} onChange={(e) => set("headers", e.target.value)} />
          </label>
          <label>
            Query params (one "name: value" per line)
            <textarea className="code" rows={2} value={ctx.params} onChange={(e) => set("params", e.target.value)} />
          </label>
        </div>
      </div>
      <h4>Result</h4>
      {!props.engine && <p className="muted">The engine is not loaded.</p>}
      {result && !result.ok && <ErrorList errors={result.errors} />}
      {result?.ok && (
        <>
          <p>
            <span className={result.changed ? "badge ok" : "badge"}>{result.changed ? "changed" : "unchanged"}</span>{" "}
            {result.applied.length > 0 ? (
              <>
                Applied rules: {result.applied.map((n, i) => (
                  <code key={i}>{n}</code>
                ))}
              </>
            ) : (
              <span className="muted">No rule applied.</span>
            )}
          </p>
          <div className="dry-grid">
            <div>
              <h4>Resulting document</h4>
              <pre className="code out">{after}</pre>
            </div>
            <div>
              <h4>Diff</h4>
              {diff.length === 0 ? (
                <p className="muted">No differences.</p>
              ) : (
                <pre className="code out diff">
                  {diff.map((l, i) => (
                    <div key={i} className={l.op === "+" ? "add" : l.op === "-" ? "del" : ""}>
                      {l.op} {l.text}
                    </div>
                  ))}
                </pre>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}
