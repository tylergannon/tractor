# YAML input

This zero-token workflow demonstrates YAML comments and literal block commands.

```sh
go run ../../cmd/tractor run multiline-tool.yaml \
  --workdir "$(mktemp -d)" \
  --logs "$(mktemp -d)/run"
```

Success prints `COMPLETED`; the verify stage independently checks the file made
by the preceding tool stage.
