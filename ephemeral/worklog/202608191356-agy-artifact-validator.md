# agy artifact-path independent validation

decision: Recorded shared and isolated runs prove agy's native write_to_file wrote the requested workspace file, then agy's terminal artifact-permission bookkeeping rejected that same path; Tractor artifact collection had not run yet.
friction: The bundled rg at /opt/homebrew/Caskroom/codex/0.148.0/codex-path/rg hung on a single known file during validation -> use git grep for bounded repository searches until the bundled binary is repaired.
