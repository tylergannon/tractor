# Attractor reference transition

decision: Use Attractor default branch revision 0aca8b748e6ecc23446fc690d2b66690b77fe0d3 as the transfer source because the requested GitHub URL resolves to the default branch; unmerged branches remain archived but are outside implementation claims.

decision: Tractor docs/spec.md already matches the source byte-for-byte at SHA-256 03075aee6d849473b5a6b1ff33a796bc9e162761916dfe8836cf7f6a36ebb3c7, so preserve the normative bytes and represent the move as an explicit ownership and provenance transition rather than rewriting the specification.

decision: Separate archive ideas that worked from designs the archive itself rejected or did not execute, so the inventory does not overstate legacy functionality.

friction: A zsh verification loop used `path` as an iterator and thereby replaced zsh's special command-search array, making `git` disappear for that shell invocation -> never use `path` as a zsh local or loop variable; use a task-specific name such as `source_file`.
