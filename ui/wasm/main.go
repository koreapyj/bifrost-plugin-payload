//go:build js && wasm

// Command wasm exposes the payload engine to the management UI.
//
// Build (see ui/build.sh):
//
//	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o engine.wasm ./ui/wasm
//
// It installs globalThis.payloadEngine with:
//
//	validate(configJSON)                -> JSON {ok, errors:[{path,message}]}
//	apply(configJSON, docJSON, ctxJSON) -> JSON {ok, body, changed, applied, errors}
//	shutdown()                          -> lets the Go program exit
//	ready                               -> true once installed
//
// ctxJSON is a payload.MatchContext and body is the resulting document text.
// If globalThis.onPayloadEngineReady is a function it is called once ready.
package main

import (
	"sync"
	"syscall/js"

	"github.com/koreapyj/bifrost-plugin-payload/ui/internal/engineapi"
)

func arg(args []js.Value, i int) string {
	if i >= len(args) || args[i].Type() != js.TypeString {
		return ""
	}
	return args[i].String()
}

func main() {
	done := make(chan struct{})
	var once sync.Once

	obj := js.Global().Get("Object").New()
	obj.Set("validate", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return engineapi.Validate(arg(args, 0))
	}))
	obj.Set("apply", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return engineapi.Apply(arg(args, 0), arg(args, 1), arg(args, 2))
	}))
	obj.Set("shutdown", js.FuncOf(func(js.Value, []js.Value) any {
		once.Do(func() { close(done) })
		return nil
	}))
	obj.Set("ready", true)
	js.Global().Set("payloadEngine", obj)

	if cb := js.Global().Get("onPayloadEngineReady"); cb.Type() == js.TypeFunction {
		cb.Invoke()
	}
	<-done
	obj.Set("ready", false)
}
