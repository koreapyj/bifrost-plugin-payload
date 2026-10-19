import { useMemo, useState } from "react";
import { QueryBuilder, type Field, type RuleGroupType, type ValueEditorProps } from "react-querybuilder";
import { checkGroup, groupToCEL, splitKeyValue, type QBGroup } from "./cel.ts";
import { FIELDS, OPERATORS, isKeyed } from "./fields.ts";
import type { FieldError } from "./types.ts";

const EMPTY: RuleGroupType = { combinator: "and", rules: [] };

function ValueEditor(props: ValueEditorProps) {
  const { field, operator, value, handleOnChange, fieldData, disabled } = props;
  const unary = operator === "null" || operator === "notNull";
  const fd = fieldData as Field & { placeholder?: string; suggestions?: string[] };
  const listId = fd.suggestions ? `sugg-${fd.name}` : undefined;
  const valuePlaceholder = operator === "in" || operator === "notIn" ? "a, b, c" : "value";
  if (isKeyed(field)) {
    const kv = splitKeyValue(value);
    const set = (k: string, v: string) => handleOnChange(unary && v === "" ? k : `${k}:${v}`);
    return (
      <span className="kv">
        <input
          className="rule-value"
          disabled={disabled}
          placeholder={fd.placeholder ?? "key"}
          value={kv.key}
          onChange={(e) => set(e.target.value.replace(/:/g, ""), kv.value)}
        />
        {!unary && (
          <input
            className="rule-value"
            disabled={disabled}
            placeholder={field === "body" ? "JSON literal or text" : valuePlaceholder}
            value={kv.value}
            onChange={(e) => set(kv.key, e.target.value)}
          />
        )}
      </span>
    );
  }
  if (unary) return null;
  return (
    <>
      <input
        className="rule-value"
        disabled={disabled}
        list={listId}
        placeholder={fd.placeholder ?? valuePlaceholder}
        value={value == null ? "" : String(value)}
        onChange={(e) => handleOnChange(e.target.value)}
      />
      {listId && (
        <datalist id={listId}>
          {fd.suggestions!.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      )}
    </>
  );
}

const controlElements = { valueEditor: ValueEditor };

export interface ConditionValue {
  cel_expression: string;
  query: QBGroup | null;
}

export function ConditionEditor(props: {
  value: ConditionValue;
  onChange: (v: ConditionValue) => void;
  errors: FieldError[];
}) {
  const { value, onChange, errors } = props;
  // Rules created through the API carry no builder tree: open them as raw CEL.
  const [mode, setMode] = useState<"builder" | "cel">(value.query || !value.cel_expression ? "builder" : "cel");
  const fields = useMemo<Field[]>(
    () => FIELDS.map((f) => ({ name: f.name, label: `${f.label}`, placeholder: f.placeholder, suggestions: f.suggestions })),
    [],
  );
  const query = (value.query as RuleGroupType | null) ?? EMPTY;
  const regexProblems = value.query ? checkGroup(value.query) : [];
  const builderReplacesCel = mode === "builder" && !value.query && !!value.cel_expression;

  const onQuery = (q: RuleGroupType) => {
    const tree = q as unknown as QBGroup;
    onChange({ query: tree, cel_expression: groupToCEL(tree) });
  };

  const onCel = (text: string) => {
    if (value.query) {
      const ok = window.confirm("Editing the raw CEL expression discards the builder tree for this rule. Continue?");
      if (!ok) return;
    }
    onChange({ query: null, cel_expression: text });
  };

  return (
    <div className="condition">
      <div className="tabs small">
        <button type="button" className={mode === "builder" ? "active" : ""} onClick={() => setMode("builder")}>
          Builder
        </button>
        <button type="button" className={mode === "cel" ? "active" : ""} onClick={() => setMode("cel")}>
          Raw CEL
        </button>
      </div>
      {mode === "builder" ? (
        <>
          {builderReplacesCel && (
            <p className="notice warn">
              This rule has a hand-written CEL expression that the builder cannot show. Adding a condition here replaces it.
            </p>
          )}
          <QueryBuilder
            fields={fields}
            operators={OPERATORS}
            query={query}
            onQueryChange={onQuery}
            controlElements={controlElements}
            showNotToggle
            resetOnFieldChange={false}
          />
          <div className="cel-preview">
            <span className="muted">CEL:</span> <code>{value.cel_expression || "(empty: always matches)"}</code>
          </div>
          {regexProblems.map((p) => (
            <p key={p} className="err">
              {p}
            </p>
          ))}
        </>
      ) : (
        <>
          <textarea
            className="code"
            rows={5}
            spellCheck={false}
            value={value.cel_expression}
            placeholder={'provider == "openai" && model.startsWith("gpt-5") && headers["x-tier"] == "pro"'}
            onChange={(e) => onCel(e.target.value)}
          />
          <p className="muted small">
            Variables: model, provider, request_type, headers, params, virtual_key_id, virtual_key_name, user_id, team_id,
            team_name, customer_id, customer_name, stage, path, from_protocol, body_format, body. An empty expression always matches.
          </p>
        </>
      )}
      {errors.map((e, i) => (
        <p key={i} className="err">
          {e.message}
        </p>
      ))}
    </div>
  );
}
