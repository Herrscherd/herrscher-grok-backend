package grok

import contracts "github.com/Herrscherd/herrscher-contracts"

const (
	modelGrok47     = "grok-4.7"
	modelGrok47Fast = "grok-4.7-build-fast"
	modelGrok46     = "grok-4.6"
	modelGrok45     = "grok-4.5"
	context256K     = 256_000
	effortLow       = "low"
	effortMedium    = "medium"
	effortHigh      = "high"
	effortXHigh     = "xhigh"
)

// Efforts returns the reasoning efforts grok accepts on --reasoning-effort, lowest first.
func Efforts() []string {
	return []string{effortLow, effortMedium, effortHigh, effortXHigh}
}

func effortsWithoutXHigh() []string {
	return []string{effortLow, effortMedium, effortHigh}
}

// Models returns the Grok Build catalogue, native route only. Prices are left
// unknown: grok reports the cost of each turn itself.
func Models() []contracts.ModelSpec {
	return []contracts.ModelSpec{
		{ID: modelGrok47, Label: "Grok 4.7", Arg: modelGrok47, Efforts: Efforts(), Route: contracts.RouteNative, ContextWindow: context256K},
		{ID: modelGrok47Fast, Label: "Grok 4.7 Fast", Arg: modelGrok47Fast, Efforts: Efforts(), Route: contracts.RouteNative, ContextWindow: context256K},
		{ID: modelGrok46, Label: "Grok 4.6", Arg: modelGrok46, Efforts: Efforts(), Route: contracts.RouteNative, ContextWindow: context256K, Hidden: true},
		{ID: modelGrok45, Label: "Grok 4.5", Arg: modelGrok45, Efforts: effortsWithoutXHigh(), Route: contracts.RouteNative, ContextWindow: context256K, Hidden: true},
	}
}
