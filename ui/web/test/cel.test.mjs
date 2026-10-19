import { test } from "node:test";
import assert from "node:assert/strict";
import { bodyExpr, bodyLiteral, celString, checkRegex, groupToCEL, listItems, ruleToCEL, splitPath } from "../src/cel.ts";
import { lineDiff } from "../src/diff.ts";

test("string escaping", () => {
  assert.equal(celString('a"b\\c\n\t'), '"a\\"b\\\\c\\n\\t"');
  assert.equal(celString("\u0001"), '"\\u0001"');
});

test("body paths", () => {
  assert.deepEqual(splitPath("a\\.b.c"), ["a.b", "c"]);
  assert.equal(bodyExpr("metadata.client"), "body.metadata.client");
  assert.equal(bodyExpr("tools.0.type"), "body.tools[0].type");
  assert.equal(bodyExpr("x-y.in"), 'body["x-y"]["in"]');
  assert.equal(bodyExpr("a..b"), null);
  assert.equal(ruleToCEL({ field: "body", operator: "notNull", value: "a.x-y" }), '"x-y" in body.a');
  assert.equal(ruleToCEL({ field: "body", operator: "notNull", value: "tools.2" }), "size(body.tools) > 2");
});

test("body literals", () => {
  assert.equal(bodyLiteral("12"), "12");
  assert.equal(bodyLiteral("1.5e3"), "1.5e3");
  assert.equal(bodyLiteral("false"), "false");
  assert.equal(bodyLiteral('"12"'), '"12"');
  assert.equal(bodyLiteral("codex"), '"codex"');
});

test("lists", () => {
  assert.deepEqual(listItems("a, b,,c"), ["a", "b", "c"]);
  assert.deepEqual(listItems('["a","b,c"]'), ["a", "b,c"]);
  assert.equal(ruleToCEL({ field: "body", operator: "in", value: "n:1,2" }), "body.n in [1, 2]");
});

test("incomplete rules and groups are dropped", () => {
  assert.equal(groupToCEL(null), "");
  assert.equal(groupToCEL({ combinator: "and", rules: [] }), "");
  assert.equal(groupToCEL({ combinator: "and", rules: [{ field: "headers", operator: "=", value: "" }, { field: "model", operator: "=", value: "m" }] }), 'model == "m"');
  assert.equal(groupToCEL({ combinator: "or", rules: [{ field: "model", operator: "=", value: "m", disabled: true }] }), "");
});

test("regex checks", () => {
  assert.equal(checkRegex("^gpt-[45]"), null);
  assert.match(checkRegex("(?<=a)b"), /lookbehind/);
  assert.match(checkRegex("(a)\\1"), /backreference/);
  assert.match(checkRegex("("), /Invalid/);
});

test("line diff", () => {
  const d = lineDiff("a\nb\nc", "a\nx\nc");
  assert.deepEqual(d.map((l) => l.op + l.text), [" a", "-b", "+x", " c"]);
  assert.deepEqual(lineDiff("same", "same"), [{ op: " ", text: "same" }]);
});
