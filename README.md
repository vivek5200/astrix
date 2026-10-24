# Astrix

[![CI](https://github.com/vivek5200/astrix/actions/workflows/ci.yml/badge.svg)](https://github.com/vivek5200/astrix/actions/workflows/ci.yml)

Astrix is a local-first DevOps verification and policy toolchain. It analyzes repository changes to select impacted tests, evaluates actions against policies written in AxLang, verifies repair patches in a network-isolated Docker sandbox, and exposes tools over MCP.

> Astrix is under active development. Review policies and generated changes before using it in production workflows.

## Components

- **`astrix` CLI:** checks `Astrixfile.ax`, builds symbol graphs, verifies changes, optionally requests a bounded repair, and serves MCP over standard input/output.
- **`astrixd` daemon:** accepts HMAC-signed webhook requests, scans diffs for credentials, and evaluates the configured policy.
- **AxLang:** the companion language and compiler for workflow, guardrail, repair, and deployment rules.

The graph layer normalizes Go, TypeScript/TSX, and Python symbols into a shared graph representation. The runtime also includes a content-addressable cache, flaky-test tracking, canary SLO evaluation, and OPA-backed policy checks.

## Requirements

- Go 1.26 or newer.
- Docker for isolated patch verification and the live repair flow.
- An OpenAI-compatible model endpoint for `astrix verify --fix`.

## Install

```sh
go install github.com/vivek5200/astrix/cmd/astrix@latest
go install github.com/vivek5200/astrix/cmd/astrixd@latest
```

To install the AxLang compiler, see [AxLang](https://github.com/vivek5200/axlang).

## Configure

Place an `Astrixfile.ax` in the repository root. For model-assisted repair, create a local `.env` file (it is ignored by Git):

```dotenv
ASTRIX_LLM_BASE_URL=http://localhost:8000/v1
ASTRIX_LLM_API_KEY=replace-with-your-local-key
ASTRIX_LLM_MODEL=your-model-name
```

`ASTRIX_LLM_ENDPOINT` may be used to specify the full chat-completions URL. The CLI also accepts `OPENROUTER_API_KEY` or `OPENAI_API_KEY`; process environment variables take precedence over `.env`. Never commit real credentials.

## Commands

```sh
astrix check [Astrixfile.ax]
astrix graph [path] [--symbol name]
astrix graph [path] --diff changes.patch
astrix verify
astrix verify --fix
astrix mcp serve
```

`astrix verify --fix` runs the repository tests first. If they fail, it requests a unified diff from the configured model, checks the patch against policy, and tests it in a disposable Docker copy with networking disabled. The repair loop is capped at three attempts and rejects test-file modifications by default.

Run the webhook daemon from a repository containing `Astrixfile.ax`:

```sh
ASTRIX_WEBHOOK_SECRET='a-long-random-secret' astrixd
```

The daemon serves `GET /healthz` and an HMAC-SHA256 authenticated `POST /webhook` on port 8080 by default. Set `ASTRIXD_ADDR` to change the listen address. The daemon does not terminate TLS; expose it only on a trusted network or behind a TLS-terminating reverse proxy, and protect the webhook secret as a credential.

## Development and tests

```sh
go test -race ./...
go vet ./...
go build ./cmd/astrix ./cmd/astrixd
```

The real Docker sandbox test runs when Docker is available. The live LLM end-to-end test reads the workspace `.env` or process environment and is skipped when no API key is configured:

```sh
go test -v ./e2e -run '^TestLiveDockerAndLLM_ClosedLoopSelfHealing$' -count=1
```

The full live demonstration is available from the workspace checkout as `scripts/live_e2e_demo.sh`.

## Related project

- [AxLang compiler](https://github.com/vivek5200/axlang)

## License

Astrix is licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
