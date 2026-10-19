import { StrictMode, useCallback, useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import "react-querybuilder/dist/query-builder.css";
import "./styles.css";
import { ApiError, ConfigStoreDisabledError, clearDraft, loadPlugin, readDraft, savePlugin, type Draft, type PluginMeta } from "./api.ts";
import { loadEngine, type Engine } from "./engine.ts";
import { CopyMode, DryRun, ErrorList, JsonTab } from "./panels.tsx";
import { RuleSheet } from "./RuleSheet.tsx";
import { SCOPES, STAGES, newKey, newRule, normalizeConfig, toConfig, type Config, type FieldError, type Rule, type RuleItem } from "./types.ts";

type Tab = "rules" | "json" | "dryrun";

function splitConfig(c: Config): { base: Config; items: RuleItem[] } {
  return { base: { ...c, rules: [] }, items: c.rules.map((rule) => ({ key: newKey(), rule })) };
}

function ruleErrors(errors: FieldError[], index: number): FieldError[] {
  const p = `rules[${index}]`;
  return errors.filter((e) => e.path === p || e.path.startsWith(p + ".") || e.path.startsWith(p + "["));
}

function scopeLabel(r: Rule): string {
  const s = r.scope || "global";
  return s === "global" ? "global" : `${s}: ${r.scope_id ?? ""}`;
}

function nextPriority(items: RuleItem[], like: Rule): number {
  let max = -1;
  for (const { rule } of items) {
    if (rule.stage === like.stage && (rule.scope || "global") === (like.scope || "global") && (rule.scope_id ?? null) === (like.scope_id ?? null)) {
      max = Math.max(max, rule.priority);
    }
  }
  return max + 1;
}

function RuleList(props: {
  items: RuleItem[];
  errors: FieldError[];
  onEdit: (key: string) => void;
  onToggle: (key: string, enabled: boolean) => void;
  onDuplicate: (key: string) => void;
  onDelete: (key: string) => void;
  onAdd: (stage: string) => void;
}) {
  const { items, errors } = props;
  const indexed = items.map((it, index) => ({ ...it, index }));
  const stages = [...STAGES, ...new Set(items.map((i) => i.rule.stage).filter((s) => !STAGES.includes(s as (typeof STAGES)[number])))];
  const scopeRank = (r: Rule) => {
    const i = SCOPES.indexOf((r.scope || "global") as (typeof SCOPES)[number]);
    return i < 0 ? SCOPES.length : i;
  };
  return (
    <div className="rule-list">
      {stages.map((stage) => {
        const inStage = indexed.filter((i) => i.rule.stage === stage);
        const groups = new Map<string, typeof inStage>();
        inStage
          .slice()
          .sort((a, b) => scopeRank(a.rule) - scopeRank(b.rule) || scopeLabel(a.rule).localeCompare(scopeLabel(b.rule)) || a.rule.priority - b.rule.priority)
          .forEach((i) => {
            const k = scopeLabel(i.rule);
            if (!groups.has(k)) groups.set(k, []);
            groups.get(k)!.push(i);
          });
        return (
          <section key={stage || "(none)"} className="stage">
            <div className="section-head">
              <h3>
                Stage <code>{stage || "(none)"}</code> <span className="muted small">{inStage.length} rule(s)</span>
              </h3>
              {STAGES.includes(stage as (typeof STAGES)[number]) && (
                <button type="button" onClick={() => props.onAdd(stage)}>
                  + Add rule
                </button>
              )}
            </div>
            {inStage.length === 0 && <p className="muted small">No rules for this stage.</p>}
            {[...groups.entries()].map(([scope, rows]) => (
              <div key={scope} className="scope-group">
                <div className="scope-title">Scope {scope}</div>
                <table>
                  <thead>
                    <tr>
                      <th className="narrow">On</th>
                      <th className="narrow">Priority</th>
                      <th>Name</th>
                      <th>Condition</th>
                      <th className="narrow">Actions</th>
                      <th />
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map(({ key, rule, index }) => {
                      const errs = ruleErrors(errors, index);
                      const a = rule.actions ?? {};
                      const n = Object.keys(a.default ?? {}).length + Object.keys(a.override ?? {}).length + (a.filter?.length ?? 0);
                      return (
                        <tr key={key} className={rule.enabled === false ? "disabled" : ""}>
                          <td>
                            <input
                              type="checkbox"
                              aria-label="Enabled"
                              checked={rule.enabled !== false}
                              onChange={(e) => props.onToggle(key, e.target.checked)}
                            />
                          </td>
                          <td>{rule.priority}</td>
                          <td>
                            <button type="button" className="link name" onClick={() => props.onEdit(key)}>
                              {rule.name || "(unnamed)"}
                            </button>
                            {rule.chain_rule && <span className="badge">chain</span>}
                            {errs.length > 0 && (
                              <span className="badge bad" title={errs.map((e) => e.message).join("\n")}>
                                {errs.length} error(s)
                              </span>
                            )}
                            {rule.description && <div className="muted small">{rule.description}</div>}
                          </td>
                          <td>
                            <code className="cel-cell">{rule.cel_expression || "always"}</code>
                          </td>
                          <td>{n}</td>
                          <td className="row-actions">
                            <button type="button" onClick={() => props.onEdit(key)}>
                              Edit
                            </button>
                            <button type="button" onClick={() => props.onDuplicate(key)}>
                              Duplicate
                            </button>
                            <button
                              type="button"
                              className="danger"
                              onClick={() => window.confirm(`Delete rule "${rule.name}"?`) && props.onDelete(key)}
                            >
                              Delete
                            </button>
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            ))}
          </section>
        );
      })}
    </div>
  );
}

function App() {
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");
  const [loadError, setLoadError] = useState("");
  const [meta, setMeta] = useState<PluginMeta>({ enabled: true, path: null });
  const [base, setBase] = useState<Config>(() => normalizeConfig({}));
  const [items, setItems] = useState<RuleItem[]>([]);
  const [saved, setSaved] = useState("");
  const [tab, setTab] = useState<Tab>("rules");
  const [editing, setEditing] = useState<string | null>(null);
  const [engine, setEngine] = useState<Engine | null>(null);
  const [engineError, setEngineError] = useState("");
  const [errors, setErrors] = useState<FieldError[]>([]);
  const [copyMode, setCopyMode] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState<{ kind: "ok" | "err"; text: string } | null>(null);

  const config = useMemo(() => toConfig(base, items), [base, items]);
  const configText = useMemo(() => JSON.stringify(config), [config]);
  const dirty = status === "ready" && configText !== saved;

  const replaceConfig = useCallback((raw: unknown) => {
    const { base: b, items: it } = splitConfig(normalizeConfig(raw));
    setBase(b);
    setItems(it);
    setEditing(null);
  }, []);

  useEffect(() => {
    loadEngine().then(setEngine, (e) => setEngineError(e instanceof Error ? e.message : String(e)));
    loadPlugin().then(
      (res) => {
        const cfg = normalizeConfig(res.config);
        replaceConfig(cfg);
        setSaved(JSON.stringify(toConfig({ ...cfg, rules: [] }, cfg.rules.map((rule) => ({ key: "", rule })))));
        setMeta(res.meta);
        setDraft(readDraft());
        setStatus("ready");
      },
      (e) => {
        if (e instanceof ApiError && e.status === 401) return; // redirecting to /login
        setLoadError(e instanceof Error ? e.message : String(e));
        setStatus("error");
      },
    );
  }, [replaceConfig]);

  useEffect(() => {
    if (!engine) return;
    const t = setTimeout(() => setErrors(engine.validate(configText).errors), 150);
    return () => clearTimeout(t);
  }, [engine, configText]);

  useEffect(() => {
    const onUnload = (e: BeforeUnloadEvent) => {
      if (dirty) e.preventDefault();
    };
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  }, [dirty]);

  const updateRule = (key: string, f: (r: Rule) => Rule) => setItems((prev) => prev.map((i) => (i.key === key ? { ...i, rule: f(i.rule) } : i)));

  const addRule = (stage: string) => {
    const r = newRule(stage);
    r.priority = nextPriority(items, r);
    const key = newKey();
    setItems([...items, { key, rule: r }]);
    setEditing(key);
  };

  const duplicate = (key: string) => {
    const src = items.find((i) => i.key === key);
    if (!src) return;
    const r: Rule = JSON.parse(JSON.stringify(src.rule));
    r.name = `${r.name}-copy`;
    r.priority = nextPriority(items, r);
    const idx = items.indexOf(src);
    setItems([...items.slice(0, idx + 1), { key: newKey(), rule: r }, ...items.slice(idx + 1)]);
  };

  const save = async () => {
    setMessage(null);
    const v = engine?.validate(configText);
    if (v && !v.ok) {
      setErrors(v.errors);
      setMessage({ kind: "err", text: `Fix ${v.errors.length} validation error(s) before saving.` });
      return;
    }
    setSaving(true);
    try {
      await savePlugin(meta, config);
      setSaved(configText);
      clearDraft();
      setDraft(null);
      setMessage({ kind: "ok", text: "Saved. Bifrost reloaded the plugin with the new configuration." });
    } catch (e) {
      if (e instanceof ConfigStoreDisabledError) {
        setCopyMode(e.message);
      } else if (!(e instanceof ApiError && e.status === 401)) {
        setMessage({ kind: "err", text: `Save failed: ${e instanceof Error ? e.message : String(e)}` });
      }
    } finally {
      setSaving(false);
    }
  };

  if (status === "loading") return <div className="center muted">Loading…</div>;
  if (status === "error")
    return (
      <div className="center">
        <p className="err">Could not load the payload plugin: {loadError}</p>
      </div>
    );

  const editingIndex = items.findIndex((i) => i.key === editing);
  const editingItem = editingIndex >= 0 ? items[editingIndex] : null;
  const globalErrors = errors.filter((e) => !e.path.startsWith("rules["));

  return (
    <div className="app">
      <header className="top">
        <div>
          <h1>Payload rules</h1>
          <span className="muted small">Bifrost payload plugin</span>
        </div>
        <div className="row">
          {engine ? (
            <span className={errors.length ? "badge bad" : "badge ok"}>{errors.length ? `${errors.length} error(s)` : "valid"}</span>
          ) : (
            <span className="badge" title={engineError}>
              {engineError ? "engine unavailable" : "loading engine…"}
            </span>
          )}
          {dirty && <span className="badge">unsaved changes</span>}
          <button type="button" className="primary" disabled={saving || !dirty} onClick={save}>
            {saving ? "Saving…" : "Save"}
          </button>
        </div>
      </header>

      {draft && (
        <div className="notice">
          An unsaved draft from {new Date(draft.savedAt).toLocaleString()} was kept when your session expired.{" "}
          <button
            type="button"
            onClick={() => {
              replaceConfig(draft.config);
              clearDraft();
              setDraft(null);
            }}
          >
            Restore draft
          </button>{" "}
          <button
            type="button"
            onClick={() => {
              clearDraft();
              setDraft(null);
            }}
          >
            Discard
          </button>
        </div>
      )}
      {message && <div className={message.kind === "ok" ? "notice ok" : "notice bad"}>{message.text}</div>}
      {engineError && <div className="notice warn">Validation and dry run are unavailable: {engineError}</div>}

      {copyMode !== null ? (
        <CopyMode config={config} message={copyMode} onBack={() => setCopyMode(null)} />
      ) : (
        <>
          <nav className="tabs">
            <button type="button" className={tab === "rules" ? "active" : ""} onClick={() => setTab("rules")}>
              Rules
            </button>
            <button type="button" className={tab === "json" ? "active" : ""} onClick={() => setTab("json")}>
              Config JSON
            </button>
            <button type="button" className={tab === "dryrun" ? "active" : ""} onClick={() => setTab("dryrun")}>
              Dry run
            </button>
          </nav>
          <ErrorList errors={globalErrors} />
          {tab === "rules" && (
            <>
              <div className="panel options">
                <label className="check">
                  <input
                    type="checkbox"
                    checked={base.passthrough_extra_params}
                    onChange={(e) => setBase({ ...base, passthrough_extra_params: e.target.checked })}
                  />
                  Passthrough extra params
                  <span className="muted small">
                    &nbsp;Send params added in the Bifrost view to the provider as extra params (client-sent extra params pass too).
                  </span>
                </label>
              </div>
              <RuleList
                items={items}
                errors={errors}
                onEdit={setEditing}
                onToggle={(key, enabled) => updateRule(key, (r) => ({ ...r, enabled }))}
                onDuplicate={duplicate}
                onDelete={(key) => setItems(items.filter((i) => i.key !== key))}
                onAdd={addRule}
              />
            </>
          )}
          {tab === "json" && <JsonTab config={config} engine={engine} onApply={replaceConfig} />}
          {tab === "dryrun" && <DryRun config={config} engine={engine} />}
        </>
      )}

      {editingItem && (
        <RuleSheet
          key={editingItem.key}
          rule={editingItem.rule}
          index={editingIndex}
          errors={ruleErrors(errors, editingIndex)}
          onChange={(r) => updateRule(editingItem.key, () => r)}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
