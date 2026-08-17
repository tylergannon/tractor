# YAML input proof

Binary `/tmp/tractor-yaml-input` was built from this task tree with SHA-256
`39f41a64cf2b3afef5c9397f2b6420d650303b17b70f96b8aac044f4e1834746`.

```sh
/tmp/tractor-yaml-input validate examples/yaml/multiline-tool.yaml
/tmp/tractor-yaml-input run examples/yaml/multiline-tool.yaml \
  --workdir ephemeral/projects/tractor/yaml-input-proof/workspace \
  --logs ephemeral/projects/tractor/yaml-input-proof/logs
```

Validation printed `valid examples/yaml/multiline-tool.yaml`. The run printed
`COMPLETED` and recorded run `e984bc5b146d88dfcc1a3e514d753156`.

The YAML literal block's `write` stage produced
`workspace/tractor-yaml-proof/result.txt` containing exactly
`YAML_MULTILINE_WORKS`. The separate `verify` stage completed, the final
checkpoint names `done` with no successor, and the timeline ends with
`PipelineCompleted`. The workflow uses only tool nodes and spent no model
tokens.
