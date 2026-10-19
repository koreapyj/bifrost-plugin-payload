import { BODY_FIELD, MAP_FIELDS } from "./cel.ts";

export interface FieldDef {
  name: string;
  label: string;
  group: "Routing" | "Payload";
  placeholder?: string;
  suggestions?: string[];
}

const PROVIDERS = [
  "openai", "anthropic", "azure", "bedrock", "vertex", "gemini", "cohere", "mistral", "groq",
  "ollama", "openrouter", "deepseek", "perplexity", "cerebras", "huggingface", "xai",
];

/** Condition fields: Bifrost Routing Rules variables plus the plugin's own variables. */
export const FIELDS: FieldDef[] = [
  { name: "model", label: "Model", group: "Routing", placeholder: "gpt-4o" },
  { name: "provider", label: "Provider", group: "Routing", suggestions: PROVIDERS },
  { name: "request_type", label: "Request type", group: "Routing", suggestions: ["chat_completion", "text_completion", "responses", "embedding"] },
  { name: "headers", label: "Header", group: "Routing", placeholder: "x-tier" },
  { name: "params", label: "Query param", group: "Routing", placeholder: "api-version" },
  { name: "virtual_key_id", label: "Virtual key ID", group: "Routing" },
  { name: "virtual_key_name", label: "Virtual key name", group: "Routing" },
  { name: "user_id", label: "User ID", group: "Routing" },
  { name: "team_id", label: "Team ID", group: "Routing" },
  { name: "team_name", label: "Team name", group: "Routing" },
  { name: "customer_id", label: "Customer ID", group: "Routing" },
  { name: "customer_name", label: "Customer name", group: "Routing" },
  { name: "stage", label: "Stage", group: "Payload", suggestions: ["http", "request", "llm"] },
  { name: "path", label: "HTTP path", group: "Payload", placeholder: "/v1/chat/completions" },
  { name: "from_protocol", label: "From protocol", group: "Payload", suggestions: ["openai", "anthropic", "genai", "bedrock", "cohere"] },
  { name: "body_format", label: "Body format", group: "Payload", suggestions: ["raw", "bifrost"] },
  { name: BODY_FIELD, label: "Body path", group: "Payload", placeholder: "metadata.client" },
];

export const OPERATORS = [
  { name: "=", label: "equals" },
  { name: "!=", label: "does not equal" },
  { name: ">", label: "greater than" },
  { name: "<", label: "less than" },
  { name: ">=", label: "greater than or equal" },
  { name: "<=", label: "less than or equal" },
  { name: "in", label: "is in list" },
  { name: "notIn", label: "is not in list" },
  { name: "contains", label: "contains" },
  { name: "beginsWith", label: "begins with" },
  { name: "endsWith", label: "ends with" },
  { name: "matches", label: "matches (RE2 regex)" },
  { name: "null", label: "does not exist" },
  { name: "notNull", label: "exists" },
];

export function isKeyed(field: string): boolean {
  return MAP_FIELDS.has(field) || field === BODY_FIELD;
}
