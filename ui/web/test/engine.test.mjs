// Runs ui/testdata/engine_cases.json through engine.wasm (built by ui/build.sh)
// and checks the UI's CEL converter against the same fixtures. The Go test
// ui/engine_cases_test.go asserts the same expectations natively.
import { after, before, describe, test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { gunzipSync } from "node:zlib";
import { runInThisContext } from "node:vm";
import { fileURLToPath } from "node:url";
import { groupToCEL } from "../src/cel.ts";

const here = (p) => fileURLToPath(new URL(p, import.meta.url));
const dist = here("../dist/");
const cases = JSON.parse(readFileSync(here("../../testdata/engine_cases.json"), "utf8"));

let engine;

before(async () => {
  for (const f of ["wasm_exec.js", "engine.wasm.gz"]) {
    if (!existsSync(dist + f)) throw new Error(`ui/web/dist/${f} is missing: run ui/build.sh first`);
  }
  runInThisContext(readFileSync(dist + "wasm_exec.js", "utf8"), { filename: "wasm_exec.js" });
  const go = new globalThis.Go();
  const wasm = gunzipSync(readFileSync(dist + "engine.wasm.gz"));
  const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
  go.run(instance); // resolves only after shutdown(); do not await
  assert.equal(globalThis.payloadEngine?.ready, true, "payloadEngine installed synchronously");
  engine = globalThis.payloadEngine;
});

after(() => engine?.shutdown());

const paths = (errors) => {
  for (const e of errors) assert.ok(e.message, "error has a message");
  return errors.map((e) => e.path);
};

describe("validate", () => {
  for (const tc of cases.validate) {
    test(tc.name, () => {
      const got = JSON.parse(engine.validate(JSON.stringify(tc.config)));
      assert.equal(got.ok, tc.ok);
      assert.deepEqual(paths(got.errors), tc.ok ? [] : tc.error_paths);
    });
  }
});

describe("apply", () => {
  for (const tc of cases.apply) {
    test(tc.name, () => {
      const doc = tc.doc_text ?? JSON.stringify(tc.doc);
      const got = JSON.parse(engine.apply(JSON.stringify(tc.config), doc, JSON.stringify(tc.ctx)));
      assert.equal(got.ok, tc.ok, JSON.stringify(got.errors));
      if (!tc.ok) {
        assert.deepEqual(paths(got.errors), tc.error_paths);
        return;
      }
      assert.deepEqual(got.errors, []);
      assert.equal(got.changed, tc.changed);
      assert.deepEqual(got.applied, tc.applied);
      assert.deepEqual(JSON.parse(got.body), tc.body);
      if (!tc.changed) assert.equal(got.body, doc);
    });
  }
});

describe("cel converter matches fixtures", () => {
  for (const tc of [...cases.validate, ...cases.apply]) {
    for (const rule of tc.config.rules ?? []) {
      if (!rule.query) continue;
      test(`${tc.name} / ${rule.name}`, () => {
        assert.equal(groupToCEL(rule.query), rule.cel_expression);
      });
    }
  }
});

test("bad inputs do not crash the engine", () => {
  const got = JSON.parse(engine.apply("{", "{}", "{}"));
  assert.equal(got.ok, false);
  assert.equal(JSON.parse(engine.validate()).ok, true, "missing argument is an empty config");
  assert.equal(JSON.parse(engine.apply("{}", "{}", "{")).errors[0].path, "context");
});
