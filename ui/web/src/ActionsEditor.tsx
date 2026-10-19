import { useState } from "react";
import type { Actions, FieldError } from "./types.ts";

interface Row {
  id: number;
  path: string;
  value: string;
}

let rowSeq = 0;

/** Shows a stored action value as the raw JSON text the user edits. */
function displayValue(v: unknown): string {
  if (typeof v === "string") return v;
  return JSON.stringify(v);
}

function jsonValid(text: string): boolean {
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}

function toRows(m: Record<string, unknown> | undefined): Row[] {
  return Object.entries(m ?? {}).map(([path, v]) => ({ id: ++rowSeq, path, value: displayValue(v) }));
}

/** Rows become a map of raw JSON fragments (the documented storage form). */
function toMap(rows: Row[]): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const r of rows) out[r.path] = r.value;
  return out;
}

function ValueTable(props: {
  title: string;
  hint: string;
  initial: Record<string, unknown> | undefined;
  onChange: (m: Record<string, unknown>) => void;
  errorFor: (path: string) => FieldError[];
}) {
  const [rows, setRows] = useState<Row[]>(() => toRows(props.initial));
  const update = (next: Row[]) => {
    setRows(next);
    props.onChange(toMap(next));
  };
  const set = (id: number, patch: Partial<Row>) => update(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));
  const counts = new Map<string, number>();
  for (const r of rows) counts.set(r.path, (counts.get(r.path) ?? 0) + 1);

  return (
    <div className="action-table">
      <div className="section-head">
        <h4>{props.title}</h4>
        <span className="muted small">{props.hint}</span>
      </div>
      {rows.length === 0 && <p className="muted small">No entries.</p>}
      {rows.map((r) => {
        const valid = jsonValid(r.value);
        const errs = props.errorFor(r.path);
        return (
          <div key={r.id} className="action-row">
            <input
              className="code path"
              placeholder="reasoning.effort"
              value={r.path}
              spellCheck={false}
              onChange={(e) => set(r.id, { path: e.target.value })}
            />
            <div className="value-cell">
              <textarea
                className={"code " + (valid ? "ok" : "bad")}
                rows={Math.min(6, Math.max(1, r.value.split("\n").length))}
                placeholder={'"high"'}
                value={r.value}
                spellCheck={false}
                onChange={(e) => set(r.id, { value: e.target.value })}
              />
              <div className="value-tools">
                <span className={valid ? "badge ok" : "badge bad"}>{valid ? "valid JSON" : "invalid JSON"}</span>
                <button type="button" className="link" onClick={() => set(r.id, { value: JSON.stringify(r.value) })}>
                  Wrap as string
                </button>
              </div>
              {(counts.get(r.path) ?? 0) > 1 && <p className="err">Duplicate path: only the last entry is kept.</p>}
              {errs.map((e, i) => (
                <p key={i} className="err">
                  {e.message}
                </p>
              ))}
            </div>
            <button type="button" className="icon" title="Remove" onClick={() => update(rows.filter((x) => x.id !== r.id))}>
              ×
            </button>
          </div>
        );
      })}
      <button type="button" onClick={() => update([...rows, { id: ++rowSeq, path: "", value: "" }])}>
        + Add path
      </button>
    </div>
  );
}

function FilterList(props: { initial: string[] | undefined; onChange: (v: string[]) => void; errorAt: (i: number) => FieldError[] }) {
  const [items, setItems] = useState<Array<{ id: number; path: string }>>(() =>
    (props.initial ?? []).map((path) => ({ id: ++rowSeq, path })),
  );
  const update = (next: typeof items) => {
    setItems(next);
    props.onChange(next.map((i) => i.path));
  };
  return (
    <div className="action-table">
      <div className="section-head">
        <h4>Filter</h4>
        <span className="muted small">Paths deleted from the document (applied last).</span>
      </div>
      {items.length === 0 && <p className="muted small">No entries.</p>}
      {items.map((it, idx) => (
        <div key={it.id} className="action-row">
          <div className="value-cell">
            <input
              className="code path"
              placeholder="metadata.debug"
              value={it.path}
              spellCheck={false}
              onChange={(e) => update(items.map((x) => (x.id === it.id ? { ...x, path: e.target.value } : x)))}
            />
            {props.errorAt(idx).map((e, i) => (
              <p key={i} className="err">
                {e.message}
              </p>
            ))}
          </div>
          <button type="button" className="icon" title="Remove" onClick={() => update(items.filter((x) => x.id !== it.id))}>
            ×
          </button>
        </div>
      ))}
      <button type="button" onClick={() => update([...items, { id: ++rowSeq, path: "" }])}>
        + Add path
      </button>
    </div>
  );
}

export function ActionsEditor(props: { actions: Actions; onChange: (a: Actions) => void; errors: FieldError[]; prefix: string }) {
  const { actions, onChange, errors, prefix } = props;
  const errorFor = (kind: string) => (path: string) => errors.filter((e) => e.path === `${prefix}.${kind}[${JSON.stringify(path)}]`);
  return (
    <div className="actions">
      <ValueTable
        title="Default"
        hint="Written only when the path is missing from the stage's original document. Values are raw JSON."
        initial={actions.default}
        onChange={(m) => onChange({ ...actions, default: m })}
        errorFor={errorFor("default")}
      />
      <ValueTable
        title="Override"
        hint="Always written (when different). Values are raw JSON."
        initial={actions.override}
        onChange={(m) => onChange({ ...actions, override: m })}
        errorFor={errorFor("override")}
      />
      <FilterList
        initial={actions.filter}
        onChange={(f) => onChange({ ...actions, filter: f })}
        errorAt={(i) => errors.filter((e) => e.path === `${prefix}.filter[${i}]`)}
      />
    </div>
  );
}
