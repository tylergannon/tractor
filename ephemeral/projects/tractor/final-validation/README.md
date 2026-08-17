# Independent final validation

This directory contains the preserved inputs, workspace effects, public run
logs, and concise report from independent live CLI validation at HEAD
`05c064f3ed1de0f0745efcae2f768c75e8f2e563`.

The `agent` binary was built once outside the repository evidence tree and was
then used for both provider-direction cases without provider or model flags.

`codex-caller-claude-aborted-capture/` preserves an earlier provider call whose
wrapper used zsh's reserved `status` variable after the call. The application
produced a result and workspace effect, but that run is excluded because its
exit code was not captured. The successful replacement is
`codex-caller-claude/`.
