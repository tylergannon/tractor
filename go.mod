module github.com/tylergannon/tractor

go 1.26

tool (
	github.com/atombender/go-jsonschema
	github.com/tylergannon/go-gen-jsonschema/gen-jsonschema
	golang.org/x/tools/cmd/goimports
	golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize
)

require (
	github.com/roasbeef/claude-agent-sdk-go v1.1.1-0.20260713164230-efdbecd88a98
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/spf13/cobra v1.10.2
	github.com/tylergannon/go-gen-jsonschema v0.11.3
	go.yaml.in/yaml/v4 v4.0.0-rc.6
)

require (
	dario.cat/mergo v1.0.2 // indirect
	github.com/atombender/go-jsonschema v0.23.1 // indirect
	github.com/dave/dst v0.27.3 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/sanity-io/litter v1.5.8 // indirect
	github.com/sosodev/duration v1.4.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tylergannon/structtag v0.1.0 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260811182544-a038080d80e5 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
