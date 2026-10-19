import { ActionsEditor } from "./ActionsEditor.tsx";
import { ConditionEditor } from "./ConditionEditor.tsx";
import { SCOPES, STAGES, type FieldError, type Rule } from "./types.ts";

const STAGE_HELP: Record<string, string> = {
  http: "Client request JSON body (HTTPTransportPreHook).",
  request: "Once per request: provider-native raw body or the Bifrost view (PreRequestHook).",
  llm: "Every attempt, including fallbacks: raw body or the Bifrost view (PreLLMHook).",
};

function Errs({ list }: { list: FieldError[] }) {
  return (
    <>
      {list.map((e, i) => (
        <p key={i} className="err">
          {e.message}
        </p>
      ))}
    </>
  );
}

export function RuleSheet(props: {
  rule: Rule;
  index: number;
  errors: FieldError[];
  onChange: (r: Rule) => void;
  onClose: () => void;
}) {
  const { rule, index, errors, onChange, onClose } = props;
  const prefix = `rules[${index}]`;
  const at = (field: string) => errors.filter((e) => e.path === `${prefix}.${field}`);
  const known = ["name", "stage", "scope", "scope_id", "priority", "cel_expression"];
  const other = errors.filter(
    (e) => !known.some((k) => e.path === `${prefix}.${k}`) && !e.path.startsWith(`${prefix}.actions`),
  );
  const set = (patch: Partial<Rule>) => onChange({ ...rule, ...patch });
  const isGlobal = (rule.scope || "global") === "global";

  return (
    <div className="sheet-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <aside className="sheet" role="dialog" aria-label="Edit rule">
        <header className="sheet-head">
          <h2>Edit rule</h2>
          <button type="button" onClick={onClose}>
            Done
          </button>
        </header>
        <div className="sheet-body">
          <Errs list={other} />
          <section>
            <div className="grid2">
              <label>
                Name
                <input value={rule.name} onChange={(e) => set({ name: e.target.value })} />
                <Errs list={at("name")} />
              </label>
              <label>
                Priority
                <input
                  type="number"
                  step={1}
                  value={Number.isFinite(rule.priority) ? rule.priority : 0}
                  onChange={(e) => set({ priority: Math.trunc(Number(e.target.value) || 0) })}
                />
                <span className="muted small">Lower runs first; unique per stage and scope.</span>
                <Errs list={at("priority")} />
              </label>
            </div>
            <label>
              Description
              <textarea rows={2} value={rule.description ?? ""} onChange={(e) => set({ description: e.target.value })} />
            </label>
            <div className="grid3">
              <label>
                Stage
                <select value={rule.stage} onChange={(e) => set({ stage: e.target.value })}>
                  {!STAGES.includes(rule.stage as (typeof STAGES)[number]) && <option value={rule.stage}>{rule.stage || "(none)"}</option>}
                  {STAGES.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
                <span className="muted small">{STAGE_HELP[rule.stage] ?? ""}</span>
                <Errs list={at("stage")} />
              </label>
              <label>
                Scope
                <select
                  value={rule.scope || "global"}
                  onChange={(e) => set({ scope: e.target.value, scope_id: e.target.value === "global" ? null : rule.scope_id ?? "" })}
                >
                  {SCOPES.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
                <Errs list={at("scope")} />
              </label>
              <label>
                Scope ID
                <input
                  disabled={isGlobal}
                  placeholder={isGlobal ? "(not used for global)" : "ID"}
                  value={rule.scope_id ?? ""}
                  onChange={(e) => set({ scope_id: e.target.value })}
                />
                <Errs list={at("scope_id")} />
              </label>
            </div>
            <div className="checks">
              <label className="check">
                <input type="checkbox" checked={rule.enabled !== false} onChange={(e) => set({ enabled: e.target.checked })} />
                Enabled
              </label>
              <label className="check">
                <input type="checkbox" checked={rule.chain_rule} onChange={(e) => set({ chain_rule: e.target.checked })} />
                Chain rule
                <span className="muted small">
                  &nbsp;After applying, re-evaluate the chain on the modified document (each rule runs at most once).
                </span>
              </label>
            </div>
          </section>
          <section>
            <h3>Condition</h3>
            <ConditionEditor
              value={{ cel_expression: rule.cel_expression, query: rule.query }}
              onChange={(v) => set(v)}
              errors={at("cel_expression")}
            />
          </section>
          <section>
            <h3>Actions</h3>
            <p className="muted small">
              Applied in order default → override → filter. Paths use gjson/sjson syntax, including #(...) queries.
            </p>
            <ActionsEditor
              actions={rule.actions ?? {}}
              onChange={(a) => set({ actions: a })}
              errors={errors.filter((e) => e.path.startsWith(`${prefix}.actions`))}
              prefix={`${prefix}.actions`}
            />
          </section>
        </div>
      </aside>
    </div>
  );
}
