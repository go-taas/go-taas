## Motivation

<!-- Why is this change needed? Link any related issues or discussions. -->

Related issue: #

## Changes

<!-- A summary of what this PR changes. -->

## Checklist

- [ ] The commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, ...)
- [ ] `make lint` passes
- [ ] `make ut` passes
- [ ] New code is covered by unit tests; critical paths (billing, protocol conversion) cover edge cases
- [ ] Protobuf contract changes are limited to `*.proto` files; generated code is **not** committed (regenerate locally with `make pbgen`)
- [ ] Documentation updated on both language sides (English `docs/`, Chinese `*.zh-cn.md`), including Mermaid diagrams and terminology

## Additional notes

<!-- Anything reviewers should pay extra attention to: migration steps, breaking changes, performance impact. -->
