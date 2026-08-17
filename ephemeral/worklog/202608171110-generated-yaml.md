correction: Replace Tractor's temporary YAML-to-JSON normalization after go-gen-jsonschema v1.0.0-rc.1 added native YAML union decoding; keep JSON and YAML decoding generated from the same registrations.
decision: Install the go-gen-jsonschema skill globally through npx skills for both Claude Code and Codex, as explicitly requested.
friction: GitHub issue reads succeed but both GraphQL and REST issue writes returned HTTP 503; retry the first_success proposal before closeout and do not claim creation until a URL is returned.
resolution: The transient GitHub failure cleared and the first_success proposal was created as tylergannon/attractor#15.
friction: go-gen-jsonschema v1.0.0-rc.1 generated owner-level YAML decoding that could not decode ordinary Optional fields or preserve strict unknown-field checks -> reported as upstream issue #53 and fixed in v1.0.0-rc.2.
decision: Use v1.0.0-rc.2 generated ValidateYAML plus UnmarshalYAML with yaml/v4 defaults; keep Tractor-specific duplicate-ID/default handling shared with JSON and do not maintain a handwritten YAML bridge.
