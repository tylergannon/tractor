module github.com/tylergannon/tractor

go 1.26

tool (
	golang.org/x/tools/cmd/goimports
	golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize
)

require github.com/santhosh-tekuri/jsonschema/v6 v6.0.2

require (
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260811182544-a038080d80e5 // indirect
	golang.org/x/text v0.14.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
)
