module github.com/tylergannon/gimble

go 1.27.1

// web/ is a Go package (it embeds the build), so without this every ./... walk
// descends into web/node_modules looking for Go packages.
ignore ./web/node_modules

// Downloaded reference source and the pinned oracle are separate workspaces.
ignore ./ephemeral/inspiration

ignore ./third_party/opencode/oracle/upstream

require (
	github.com/coder/websocket v1.8.13
	github.com/roasbeef/claude-agent-sdk-go v1.1.1-0.20260713164230-efdbecd88a98
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/tylergannon/polytype v1.0.0-rc.12.0.20260911210434-38f05b1ba899
	github.com/tylergannon/skgo v0.4.0
	golang.org/x/sync v0.23.0
)

replace github.com/roasbeef/claude-agent-sdk-go => github.com/tylergannon/claude-agent-sdk-go v1.1.1-0.20260912021749-9a4ffeca77cc

require (
	github.com/dave/dst v0.27.4 // indirect
	github.com/dlclark/regexp2/v2 v2.8.0 // indirect
	github.com/dop251/goja v0.0.0-20260911104922-fabc3b8078ad // indirect
	github.com/dop251/goja_nodejs v0.0.0-20260212111938-1f56ff5bcf14 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/google/pprof v0.0.0-20260906184651-6331bc6350fe // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/tylergannon/structtag v0.1.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/tools v0.50.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

tool (
	github.com/tylergannon/polytype/polytype
	// skgo generates the bindings, and projects the Go types that cross to
	// TypeScript through polytype's library. It runs through `go tool`, so it is
	// built from the module cache and does not have to be a writable checkout.
	github.com/tylergannon/skgo/cmd/skgo
)
