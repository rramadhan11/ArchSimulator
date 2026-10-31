//go:build js && wasm

// Command wasm exposes the engine to the browser. It registers
// globalThis.archsim with three functions that take and return JSON strings:
//
//	archsim.catalog()          -> {"result": Catalog}
//	archsim.run(designJSON)    -> {"result": Result}   | {"error": "..."}
//	archsim.simulate(design)   -> {"result": Timeline} | {"error": "..."}
//
// Calls are synchronous; the web app runs them inside a Web Worker.
package main

import (
	"encoding/json"
	"errors"
	"syscall/js"

	"archsim/engine/catalog"
	"archsim/engine/model"
	"archsim/engine/sim"
)

type response struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func reply(v any, err error) any {
	r := response{Result: v}
	if err != nil {
		r = response{Error: err.Error()}
	}
	data, err := json.Marshal(r)
	if err != nil {
		data, _ = json.Marshal(response{Error: "encode result: " + err.Error()})
	}
	return string(data)
}

// withDesign parses the first argument as a design and passes it to fn.
func withDesign(fn func(*model.Architecture, *catalog.Catalog) (any, error)) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) any {
		cat, err := catalog.Load()
		if err != nil {
			return reply(nil, err)
		}
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return reply(nil, errArgs)
		}
		a, err := model.Parse([]byte(args[0].String()))
		if err != nil {
			return reply(nil, err)
		}
		return reply(fn(a, cat))
	})
}

var errArgs = errors.New("expected one argument: the design as a JSON string")

func main() {
	api := map[string]any{
		"catalog": js.FuncOf(func(js.Value, []js.Value) any {
			cat, err := catalog.Load()
			return reply(cat, err)
		}),
		"run": withDesign(func(a *model.Architecture, cat *catalog.Catalog) (any, error) {
			return sim.Run(a, cat)
		}),
		"simulate": withDesign(func(a *model.Architecture, cat *catalog.Catalog) (any, error) {
			return sim.Simulate(a, cat)
		}),
	}
	js.Global().Set("archsim", js.ValueOf(api))
	select {} // keep the exported functions alive
}
