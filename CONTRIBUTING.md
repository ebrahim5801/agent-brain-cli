# Contributing

Thanks for your interest. A few things worth knowing before you open a PR.

## License

agent-brain is under the Business Source License 1.1, not an OSI-approved open
source license. By contributing you agree your contribution is licensed under
the same terms.

## Ground rules

- **Go 1.25+.** `go build ./...`, `go vet ./...`, and `go test ./...` must all
  be clean before you push.
- **The wire boundary is not negotiable.** `wire/` defines everything that can
  leave the machine. Guard tests fail the build if a field for a path, remote,
  project name, hostname, or message text appears there, or if memory content
  reaches a transport package. If your change needs a new wire field, say why
  in the PR — that's a design conversation, not a rubber stamp.
- **Migrations are forward-only.** Add a new migration; never edit an existing
  one. Every new table needs a row in `docs/compliance/retention.md` or the
  guard test fails.
- **Match the surrounding code.** Comment density, naming, and idiom.

## Reporting bugs

Open an issue with your OS, `agent-brain --version`, the assistant you're using,
and the output of `agent-brain status`. Redact anything sensitive before
posting.

## Security

Do not open a public issue for security problems. See [SECURITY.md](SECURITY.md).
