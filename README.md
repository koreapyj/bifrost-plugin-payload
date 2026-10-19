# bifrost-plugin-payload

A [Bifrost](https://github.com/maximhq/bifrost) plugin that rewrites request
payloads with declarative rules, in the spirit of the `payload:` section of
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). Rules are selected
with CEL conditions and a priority/scope chain (the same model as Bifrost
Routing Rules) and edit the JSON document with `default`, `override` and
`filter` actions on gjson/sjson paths.

It also ships a small management UI (rule editor, validation and dry-run in the
browser via a WebAssembly build of the engine) served by the plugin itself.

## Why three stages

Bifrost has no plugin hook that sees the final, serialized provider body. The
plugin therefore applies rules at three points of the pipeline, and you choose
the one that fits each rule:

| `stage`   | Hook                    | Document edited                                                                 | Runs                           | `provider` / `model` come from |
|-----------|-------------------------|----------------------------------------------------------------------------------|--------------------------------|--------------------------------|
| `http`    | `HTTPTransportPreHook`  | The client's JSON request body, before Bifrost parses it                        | once per HTTP request           | body `model` (`provider/model`), else the integration path prefix |
| `request` | `PreRequestHook`        | Raw provider body when Bifrost forwards the body verbatim, otherwise the Bifrost view | once per request, before fallbacks | the parsed request |
| `llm`     | `PreLLMHook`            | Same as `request`                                                                 | once per attempt (primary and every fallback) | the provider/model of that attempt |

- `http` sees exactly what the client sent (`body_format` is `raw`). Keys
  Bifrost does not know become `ExtraParams` when it parses the body and are
  dropped on the way to the provider unless extra-param passthrough is on (see
  below). Governance IDs are not known yet at this point, so only `global`
  rules can match at the `http` stage.
- `request` and `llm` see a document whose `body_format` is either:
  - `raw`: Bifrost forwards the client body to the provider as-is
    (`UseRawRequestBody`). Edits are the final provider body. In Bifrost
    v2.3.x this happens for Claude Code clients (user agent `claude-cli`,
    `claude-code` or `claude-vscode`) calling `/anthropic/v1/messages` for an
    Anthropic model, and for some `/genai` routes. Bifrost still replaces
    `model` with the resolved model name.
  - `bifrost`: everything else. The document is the **Bifrost view** of the
    internal request (see below).
- HTTP-stage information (headers, path, integration protocol) is stored on the
  request context, so `request` and `llm` rules can match on it too.

For the HTTP stage the provider is resolved as follows:

1. A body `model` of the form `provider/model` is split when `provider` is a
   provider Bifrost knows (the same rule as Bifrost's own model parsing).
2. Otherwise the integration route decides: `/openai` -> `openai`,
   `/anthropic` -> `anthropic`, `/genai` -> `gemini`, `/bedrock` -> `bedrock`,
   `/cohere` -> `cohere`, the `*_passthrough` variants of these, and
   `/azure_passthrough` -> `azure`. The multi-dialect SDK prefixes
   (`/litellm`, `/langchain`, `/pydanticai`, `/cursor`) are classified by the
   path below them (`/v1/messages` -> Anthropic, `/v1beta/...` -> GenAI,
   `/model/...` -> Bedrock, `/v2/...` -> Cohere). For GenAI and Bedrock routes
   the model comes from the path.
3. Otherwise (for example plain `/v1/chat/completions` with a bare model name)
   the provider is empty and conditions on `provider` do not match.

`from_protocol` is the API dialect of the route: `openai` (also plain `/v1/*`),
`anthropic`, `genai`, `bedrock` or `cohere`.

Requests handled in large-payload mode are skipped and logged: when the body
is empty but the request has `Content-Length > 0` or `Transfer-Encoding`, the
body is streamed and not available to rewrite.

## Install

Go plugins (`-buildmode=plugin`) can only be loaded by a host binary built from
the **same source tree, the same dependency versions and the same Go
toolchain**. Official Bifrost release binaries are statically linked and cannot
load plugins at all. Each `bifrost-v<bifrost>` release of this repository
therefore ships a pair that must be used together (same `<commit>`):

- `bifrost-http-v<bifrost>-<commit>-linux-<arch>`: dynamically linked Bifrost
  HTTP gateway built from the `transports/v<bifrost>` tag, with the dashboard.
- `payload-<commit>-bifrost-v<bifrost>-linux-<arch>.so`: the plugin, built in
  the same Go workspace and toolchain.
- `SHA256SUMS-<commit>-linux-<arch>.txt`.

Architectures: `amd64`, `arm64` (glibc, built on Ubuntu 24.04; cgo is required
by both binaries).

Run the provided `bifrost-http` in place of the official binary and add the
plugin to `config.json`:

```json
{
  "plugins": [
    {
      "enabled": true,
      "name": "payload",
      "path": "/opt/bifrost/plugins/payload-0cc1a99864ec-bifrost-v2.3.1-linux-amd64.so",
      "config": { "rules": [], "ui": { "enabled": true, "api_base": "http://127.0.0.1:8080" } }
    }
  ]
}
```

[`config.example.json`](config.example.json) is a complete example of **one
entry of the `plugins` array** (not a whole Bifrost `config.json`). With a
config store the plugin can also be added or edited from the Bifrost dashboard
or via `PUT /api/plugins/payload`.

Loading fails with an error such as `plugin was built with a different version
of package ...` when the `.so` and the host do not match. Upgrading Bifrost
means upgrading both files.

Notes:

- Keep the default `post_builtin` placement for custom plugins. With
  `pre_builtin` the plugin runs before Bifrost's governance plugin, and the
  governance variables (virtual key, user, team, customer) and the matching
  scopes are not available at the `request` and `llm` stages.
- Saving the configuration (`PUT /api/plugins/payload`, the dashboard or the
  plugin UI) calls `Init` again on the already loaded `.so`; the new rules take
  effect immediately. Go cannot unload plugins, so replacing the `.so` file
  requires a restart of `bifrost-http`. Use a local file path for `path`: a
  plugin loaded from a URL cannot be reloaded ("plugin already loaded").

### Kubernetes (official Bifrost Helm chart)

CI also publishes a container image to `ghcr.io/koreapyj/bifrost`
(linux/amd64 and linux/arm64). It has the same layout as the official Bifrost
image (`/app/main`, the same entrypoint, `APP_DIR=/app/data`, UID 1000, port
8080) with the matching `bifrost-http` and the plugin at
`/app/plugins/payload.so`, so the official chart only needs a different image
and a `custom` plugin entry:

```yaml
image:
  repository: ghcr.io/koreapyj/bifrost
  tag: v2.3.1                  # or v2.3.1-<commit> to pin a build

bifrost:
  plugins:
    custom:
      - name: payload
        enabled: true
        path: /app/plugins/payload.so
        config:
          rules: []
          ui:
            enabled: true
            api_base: http://127.0.0.1:8080   # same pod; match the container port
```

Image tags:

- `v<version>`: newest plugin commit built against that Bifrost version.
- `v<version>-<commit>`: immutable.
- `latest`: newest plugin commit on the newest Bifrost version.

Packages on GitHub Container Registry are private when first created. Either
make the package public in its settings or add an `imagePullSecrets` entry.
The image base is Ubuntu 24.04 (glibc), not Alpine: both binaries are built
with cgo on Ubuntu runners.

## Configuration reference

The object passed as the plugin `config` (JSON, snake_case):

| Field | Type | Default | Description |
|---|---|---|---|
| `rules` | array of rules | `[]` | Rewriting rules (below). |
| `passthrough_extra_params` | bool | `false` | When a Bifrost-view edit produces extra params, set Bifrost's passthrough flag so they are sent to the provider. |
| `ui.enabled` | bool | `false` | Serve the management UI. |
| `ui.path` | string | `/v1/models/_payload/ui` | Mount path of the UI. |
| `ui.api_base` | string | none | Base URL the plugin uses to reach this Bifrost instance for the session check, e.g. `http://127.0.0.1:8080`. Without it the UI stays hidden. |

Rule fields:

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | required | Rule name (shown in logs and the UI). |
| `description` | string | `""` | Free text. |
| `enabled` | bool | `true` | Disabled rules are ignored. |
| `stage` | `http` \| `request` \| `llm` | required | Where the rule runs (see above). |
| `scope` | `virtual_key` \| `user` \| `team` \| `customer` \| `global` | `global` | Scope of the rule. |
| `scope_id` | string \| null | `null` | ID of the virtual key / user / team / customer. Must be `null` for `global`. |
| `priority` | int | `0` | Lower runs first. Unique per (`scope`, `scope_id`, `stage`). |
| `chain_rule` | bool | `false` | After this rule applies, re-evaluate the chain on the modified document. |
| `cel_expression` | string | `""` | CEL condition. Empty always matches. |
| `query` | object \| null | `null` | Condition builder tree saved by the UI. Ignored by the engine. |
| `actions.default` | object | `{}` | `path -> raw JSON value`, written only when the path is absent in the stage's original document. |
| `actions.override` | object | `{}` | `path -> raw JSON value`, written when the current value differs. |
| `actions.filter` | array of paths | `[]` | Paths to delete. |

`Init` validates the whole configuration and fails without changing the running
rules when anything is invalid: unknown fields, invalid stage/scope, duplicate
priorities, CEL compile errors, invalid JSON values. Reloading the plugin
(`PUT /api/plugins/payload` or the UI) swaps the rule set atomically.

### Conditions (CEL)

Conditions use [cel-go](https://github.com/google/cel-go) with the same
variables as Bifrost Routing Rules plus plugin-specific ones:

| Variable | Type | Description |
|---|---|---|
| `model`, `provider` | string | Target model and provider (see the stage table). |
| `request_type` | string | Bifrost request type, e.g. `chat_completion`, `chat_completion_stream`, `responses`, `responses_stream`, `embedding`. |
| `headers` | map(string, string) | Request headers, lower-case keys. |
| `params` | map(string, string) | Query parameters, lower-case keys. |
| `virtual_key_id`, `virtual_key_name` | string | Governance virtual key. |
| `user_id` | string | Governance user. |
| `team_id`, `team_name` | string | Governance team. |
| `customer_id`, `customer_name` | string | Governance customer. |
| `stage` | string | `http`, `request` or `llm`. |
| `path` | string | HTTP request path, e.g. `/v1/chat/completions`. |
| `from_protocol` | string | API dialect of the route: `openai`, `anthropic`, `genai`, `bedrock` or `cohere`. |
| `body_format` | string | `raw` or `bifrost` (`raw` at the HTTP stage). |
| `body` | dyn | The current document, e.g. `body.metadata.client == "codex"`, `has(body.tools)`, `body.tools.exists(t, t.type == "web_search")`. |

Map keys in expressions are normalized like Routing Rules do:
`headers["X-Tier"]` is the same as `headers["x-tier"]`. An expression must
evaluate to a bool; a runtime evaluation error (for example a missing map key)
or a non-bool result counts as "no match". As in Routing Rules, no CEL
extension libraries are enabled.

Governance variables (and therefore the `virtual_key`, `user`, `team` and
`customer` scopes) are filled in by Bifrost's governance plugin in its
`PreRequestHook`. They are available to `request` and `llm` rules when the
payload plugin runs after the built-in plugins (the default `post_builtin`
placement) and are empty at the `http` stage, where only `global` rules match.

### Evaluation: scope chain, priority, first match, chain_rule

For every stage the engine walks the scope chain from the most specific scope
to the least specific one: `virtual_key` -> `user` -> `team` -> `customer` ->
`global`. Only scopes whose ID is present on the request take part; `global`
always comes last. Inside a scope, rules run in ascending `priority`.

- **First match wins**: the first rule whose condition matches is applied and
  the stage ends.
- **`chain_rule: true`**: after the rule is applied, the chain is evaluated
  again from the start on the modified document. Each rule runs at most once
  per request and stage, and at most 10 rules fire per stage. Use it to stack
  several rules.

### Actions

`default` and `override` values are **raw JSON** (the `*-raw` semantics of
CLIProxyAPI):

- A JSON string is used verbatim as a JSON fragment: `"\"high\""` writes the
  string `high`, `"{\"type\":\"json_object\"}"` writes an object, `"32768"`
  writes a number.
- Any other JSON value (number, bool, object, array) is written as is:
  `32768`, `true`, `{"type": "enabled"}`. Numbers keep their exact JSON text.
- A JSON `null` writes `null` (CLIProxyAPI skips nil values instead; use
  `filter` to remove a key).

Order inside one rule: `default`, then `override`, then `filter`.

- `default` writes only when the path is absent in the stage's **original**
  document (the document before any rule of this stage ran). In a chain the
  earlier rule wins.
- `override` writes when the value differs. In a chain the later rule wins.
- `filter` deletes paths (array indices are removed from the highest index
  down).
- Within `default` and `override`, paths are applied in sorted order.
- A document that is empty or not valid JSON is passed through unchanged.

Paths are [gjson](https://github.com/tidwall/gjson)/[sjson](https://github.com/tidwall/sjson)
paths (`a.b.0.c`, escaped dots `a\.b`), extended with gjson queries:
`tools.#(type=="web_search")` addresses the first matching element and
`messages.#(role=="system")#.content` every matching element.

### The Bifrost view (`body_format == "bifrost"`)

When the provider body is not available, `request` and `llm` rules edit a JSON
view of Bifrost's internal request (`ChatRequest`, `ResponsesRequest`,
`EmbeddingRequest`, ...):

```json
{
  "provider": "openai",
  "model": "gpt-4o-mini",
  "input": [ ... ],
  "params": { "temperature": 0.5, "metadata": { "a": "b" }, "my_extra": 1 },
  "fallbacks": [ { "provider": "anthropic", "model": "claude-sonnet-4" } ]
}
```

- `params` contains the typed parameters plus the request's `ExtraParams`
  flattened into the same object.
- Editable top-level keys: `model` and `params`; at the `request` stage also
  `provider` and `fallbacks`. Edits to anything else (for example the
  messages/`input`) are dropped with a warning. `provider` edits at the `llm`
  stage are ignored because the provider is already chosen.
- A `params` key that the parameter type does not consume becomes an
  **extra param**. Bifrost only sends extra params to the provider when its
  passthrough flag is set; with `passthrough_extra_params: true` the plugin
  sets it whenever a rule adds an extra param. Side effect: extra params sent
  by the client in the same request are passed through as well.
- Use the parameter names of Bifrost's types. For chat requests use
  `params.max_completion_tokens`; `params.max_tokens` is not a chat parameter
  and ends up as an extra param.
- Fields not represented in the view (`json:"-"`) are preserved.
- Rewritten `fallbacks` lose Bifrost's internal "inferred" marker on each
  entry.
- The passthrough flag is set on the request context, which is shared by all
  attempts: once set, it also applies to fallback attempts of that request.

### Fallback isolation

Bifrost builds fallback attempts as shallow copies of the original request, so
they share parameter structs and maps. The plugin never writes through the
request it receives: every change produces a new request with new inner
structs (new raw body slice, new `Params`, new `ExtraParams` map). An `llm`
rule that matches only the primary provider therefore never leaks into the
fallback attempt, and changes made by other plugins earlier in the pipeline are
kept.

## Management UI

With `ui.enabled` and `ui.api_base` set, the UI is served at `ui.path`
(default `GET /v1/models/_payload/ui`, assets below it). It is served from
`HTTPTransportPreAuthHook`, before Bifrost's own authentication, because
browsers do not send virtual keys.

- **Session gate**: a request is served only when it carries a valid Bifrost
  dashboard session (the `token` cookie set by the dashboard login, or
  `Authorization: Bearer <session token>`). Requests without either credential
  are rejected without any network call. Otherwise the plugin forwards the
  `Cookie` and `Authorization` headers to
  `GET {api_base}/api/session/is-auth-enabled` (2 s timeout) and requires
  `is_auth_enabled` and `has_valid_token` to be true. Decisions, including
  denials, are cached for 30 s per credential hash (at most 1024 entries).
  Log in to the Bifrost dashboard first, then open the UI path in the same
  browser.
- Only `GET` is served; any other method (including `HEAD`) goes to Bifrost.
  Assets: `index.html` (also served at the path itself), `app.js`, `app.css`,
  `wasm_exec.js`, `engine.wasm`. The page uses a `<base href>` for the mount
  path, so `ui.path` can be changed freely.
- **Stealth**: in every other case (no or invalid token, dashboard auth
  disabled, `api_base` unset, loopback error) the plugin does nothing and the
  request continues to Bifrost's `GET /v1/models/{model}` handler. The response
  is byte-for-byte the one for an unknown model; only timing can differ.
- `ui.path` must be a clean absolute path (`Init` rejects anything else). It
  should stay under `/v1/models/`, the catch-all route that always passes
  through plugin middleware, and its first segment there should not be a
  provider name (the default `_payload` is fine); otherwise requests that fail
  the gate turn into a model lookup at that provider. The plugin warns in both
  cases.
- `api_base` is configuration only (the `Host` header is never trusted, so a
  forged host cannot leak session cookies). Point it at an address where this
  Bifrost instance is reachable from itself, usually `http://127.0.0.1:<port>`.
- All UI responses carry `Cache-Control: no-store`, a Content-Security-Policy
  and `X-Content-Type-Options: nosniff`. `engine.wasm` is embedded
  gzip-compressed and sent with `Content-Encoding: gzip`, or decompressed for
  clients that do not accept gzip.
- On a cache miss the loopback call adds latency to the response, which is
  the remaining observable difference for unauthenticated probes.
- The UI reads and saves the configuration through Bifrost's own API
  (`GET`/`PUT /api/plugins/payload`), keeping `enabled`, `path`, `placement`
  and `order` and always sending the complete `config` (Bifrost merges
  top-level config keys, so a partial object would keep stale keys). When the
  session expires (401) it stores the draft in `sessionStorage` and sends you
  to `/login`. Without a config store ("configstore is disabled", 400) it
  offers the JSON to paste into `config.json`.
- Validation and dry-run run in the browser: the engine compiled to
  WebAssembly validates the configuration with the same CEL compiler and
  applies rules to a sample document (`http`/`raw` documents, or a Bifrost
  view you paste).

**Security notes**

- The UI path is public: anyone can request it, and only the session check
  decides whether the page is served. The page itself contains no secrets.
- Saving relies entirely on Bifrost's dashboard authentication of
  `/api/plugins/*`. If dashboard authentication is disabled, the UI will not
  open (no valid token can exist), but the plugin API is then open to anyone
  who can reach the management API: rules and `ui.api_base` can be changed
  without credentials (Bifrost only demands real authentication for plugin
  `path` changes and for its known endpoint keys, which do not include
  `api_base`). Keep dashboard authentication enabled.

## Building

Requirements: Go (the version in Bifrost's `transports/go.mod`), a C toolchain
(cgo), Node.js 22.18 or newer and npm (required by `ui/web`), curl, jq (for
the e2e test).

```sh
# Bifrost source at a release tag
mkdir -p ../bifrost
curl -fsSL https://codeload.github.com/maximhq/bifrost/tar.gz/refs/tags/transports/v2.3.1 \
  | tar -xz --strip-components=1 -C ../bifrost

make assets                                   # engine.wasm.gz, wasm_exec.js, UI bundle (ui/build.sh)
make test test-web                            # go vet + go test, Node test of the wasm engine
make bifrost-ui BIFROST_DIR=../bifrost        # Bifrost dashboard (or bifrost-ui-placeholder for tests)
make bifrost-http so BIFROST_DIR=../bifrost   # .tmp/build/out/{bifrost-http,payload.so}
make e2e                                      # end-to-end test with a mock upstream
```

`make so` and `make bifrost-http` share one `go.work` (`.tmp/build/go.work`)
containing `<bifrost>/transports` and this module, so both binaries are built
from a single dependency graph. CI additionally runs `make sync-core`, which
pins `github.com/maximhq/bifrost/core` in `go.mod` to the version required by
the selected `transports` tag so the workspace never upgrades the host's core.
The `bifrost/core` version in this repository's `go.mod` is only a development
default: `transports/v2.3.1` pins `bifrost/core v1.13.0` (not `v1.12.0`, as an
earlier plan assumed), and CI always derives the version from
`bifrost/transports/go.mod` of the tag it builds.

`make help` lists all targets; `BUILD_DIR`, `OUT_DIR` and `BIFROST_VERSION`
can be overridden.

### End-to-end test

`e2e/run.sh` starts `e2e/mock-upstream` (an OpenAI/Anthropic-compatible server
that records every request) and `bifrost-http` with `e2e/config.json`, then
checks what reached the provider: default/override/filter, priority and first
match, `chain_rule`, scope chain with two virtual keys, every hook, provider
specific `llm` rules, fallback isolation, the raw Anthropic body, UI serving,
stealth responses and hot reload. It needs `BIFROST_HTTP` and `PAYLOAD_SO`
(set by `make e2e`); see the header of the script for optional settings.

### CI

`.github/workflows/build.yml`:

- push / pull request: UI assets, `go vet`, `go test`, Node engine test.
- push to the default branch, daily schedule and `workflow_dispatch` (no
  inputs), default branch only:
  selects the latest four stable `transports/v*` tags (`BIFROST_VERSIONS_COUNT`)
  and, for each version on `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm`
  (arm64), downloads the Bifrost tarball, syncs the core version, builds the
  dashboard, `bifrost-http` and `payload.so`, runs the end-to-end test and
  publishes the binaries to an automatically created release
  `bifrost-v<version>`. Asset names contain the plugin commit
  (`payload-<commit>-bifrost-v<version>-linux-<arch>.so`); a combination whose
  assets already exist is skipped, and a newer commit replaces older assets.
- Only the latest Bifrost version is required to build; older versions may
  lack APIs the plugin uses, and their failures do not fail the run.

## Limitations

- Only the native Linux builds produced together with a matching
  `bifrost-http` work; the plugin cannot be loaded into official Bifrost
  binaries, Docker images or other Go toolchains.
- The final provider body is only editable when Bifrost forwards the client
  body verbatim (`body_format == "raw"`). Otherwise edits are limited to
  `model`, `params` (and `provider`/`fallbacks` at the request stage) of the
  Bifrost view, and Bifrost's provider conversion decides how they are
  serialized; parameters a provider does not support may be dropped by Bifrost.
- Extra-param passthrough is request-wide (see the Bifrost view).
- Bodies not held in memory (large-payload mode) are not rewritten.
- The UI dry-run covers `http`/`raw` documents directly; Bifrost-view rules are
  simulated on a view document you provide.

## Credits and licenses

- Payload rule semantics (`default`/`override`/`filter`, raw JSON values,
  gjson/sjson paths and queries) follow
  [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) (MIT License).
  The behavior was reimplemented; no code was copied.
- Condition, scope and priority semantics follow Bifrost Routing Rules. The CEL
  map-key normalization reimplements the behavior of
  `framework/routing.NormalizeMapKeysInCEL`, and the UI's condition-to-CEL
  converter was written with Bifrost's `celConverterRouting.ts` as a reference.
  [Bifrost](https://github.com/maximhq/bifrost) is licensed under the Apache
  License 2.0.
- The release `bifrost-http` binaries are unmodified builds of Bifrost
  (Apache-2.0); see the Bifrost repository for its license and notices.

This project is licensed under the MIT License (see [LICENSE](LICENSE)).
