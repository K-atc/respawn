# AGENTS.md

This repository is intended to be published as a **public GitHub repository**.

## Public Repository Safety

- Do not commit personal information, hostnames, local usernames, private paths, tokens, credentials, API keys, or secrets.
- Do not include environment-specific logs or command output that may reveal private infrastructure.
- Avoid absolute local paths in documentation and examples. Prefer generic paths such as `./bin` or `/usr/local/bin` only when appropriate.
- Keep commit author information generic when creating or rewriting commits for publication.
- Before committing, check for accidental sensitive data with a search such as:

```bash
grep -RInE 'token|secret|password|api[_-]?key|private key|BEGIN .*PRIVATE|/home/|hostname' . --exclude-dir=.git
```

## Development

- This is a small Go command-line wrapper.
- Keep the implementation dependency-free unless there is a clear reason to add a dependency.
- Run these checks before committing:

```bash
go test ./...
go build -o /tmp/codex-remote-control-respawn-check .
```

## Git Hygiene

- Do not commit built binaries or temporary files.
- Keep `.gitignore` updated for generated artifacts.
- Use concise commit messages that describe the user-visible change.
