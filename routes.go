package main

import "strings"

// from_protocol values: the client-facing API dialect of the HTTP route.
const (
	protocolOpenAI    = "openai"
	protocolAnthropic = "anthropic"
	protocolGenAI     = "genai"
	protocolBedrock   = "bedrock"
	protocolCohere    = "cohere"
)

// routeInfo describes what the http stage can infer from a request path.
type routeInfo struct {
	protocol   string // from_protocol
	provider   string // provider implied by the integration route, or ""
	modelParam string // router path parameter carrying the model, or ""
}

// protocolProvider maps a dialect to the provider its dedicated integration
// route targets.
var protocolProvider = map[string]string{
	protocolOpenAI:    "openai",
	protocolAnthropic: "anthropic",
	protocolGenAI:     "gemini",
	protocolBedrock:   "bedrock",
	protocolCohere:    "cohere",
}

// providerPrefixes are the single-provider integration prefixes of
// transports/bifrost-http/integrations (and their *_passthrough variants).
var providerPrefixes = map[string]string{
	"openai":                protocolOpenAI,
	"anthropic":             protocolAnthropic,
	"genai":                 protocolGenAI,
	"bedrock":               protocolBedrock,
	"cohere":                protocolCohere,
	"openai_passthrough":    protocolOpenAI,
	"anthropic_passthrough": protocolAnthropic,
	"genai_passthrough":     protocolGenAI,
	"bedrock_passthrough":   protocolBedrock,
}

// passthroughProviders covers passthrough prefixes whose dialect differs from
// the provider name.
var passthroughProviders = map[string]routeInfo{
	"azure_passthrough":   {protocol: protocolOpenAI, provider: "azure"},
	"chatgpt_passthrough": {protocol: protocolOpenAI},
}

// multiDialectPrefixes are SDK integrations that mount several dialects under
// one prefix (e.g. /litellm/v1/messages is Anthropic, /litellm/v1beta/... is
// GenAI). They do not imply a provider for the OpenAI dialect.
var multiDialectPrefixes = map[string]bool{
	"litellm":    true,
	"langchain":  true,
	"pydanticai": true,
	"cursor":     true,
}

// classifyPath derives from_protocol, the integration provider and the model
// path parameter from a request path. Routes without an integration prefix
// (/v1/...) are the OpenAI-compatible API and imply no provider.
func classifyPath(path string) routeInfo {
	first, rest := splitFirst(path)
	if proto, ok := providerPrefixes[first]; ok {
		return withModelParam(routeInfo{protocol: proto, provider: protocolProvider[proto]})
	}
	if ri, ok := passthroughProviders[first]; ok {
		return ri
	}
	if multiDialectPrefixes[first] {
		if proto, ok := providerPrefixes[firstSegment(rest)]; ok {
			return withModelParam(routeInfo{protocol: proto, provider: protocolProvider[proto]})
		}
		proto := dialectOfSubpath(rest)
		ri := routeInfo{protocol: proto}
		if proto != protocolOpenAI {
			ri.provider = protocolProvider[proto]
		}
		return withModelParam(ri)
	}
	return routeInfo{protocol: protocolOpenAI}
}

// dialectOfSubpath classifies the path below a multi-dialect prefix using the
// route shapes registered by each integration.
func dialectOfSubpath(p string) string {
	switch {
	case p == "/v1/messages" || strings.HasPrefix(p, "/v1/messages/") || p == "/v1/complete":
		return protocolAnthropic
	case strings.HasPrefix(p, "/v1beta/") || strings.HasPrefix(p, "/upload/v1beta/"):
		return protocolGenAI
	case strings.HasPrefix(p, "/model/"):
		return protocolBedrock
	case strings.HasPrefix(p, "/v2/"):
		return protocolCohere
	default:
		return protocolOpenAI
	}
}

func withModelParam(ri routeInfo) routeInfo {
	switch ri.protocol {
	case protocolGenAI:
		ri.modelParam = "model" // /v1beta/models/{model:*}
	case protocolBedrock:
		ri.modelParam = "modelId" // /model/{modelId}/converse
	}
	return ri
}

// splitFirst splits "/a/b/c" into "a" and "/b/c".
func splitFirst(path string) (string, string) {
	p := strings.TrimPrefix(path, "/")
	first, rest, found := strings.Cut(p, "/")
	if !found {
		return first, ""
	}
	return first, "/" + rest
}

func firstSegment(path string) string {
	first, _ := splitFirst(path)
	return first
}
