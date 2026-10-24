# Contributing to Astrix

Thanks for helping improve Astrix. Changes should be focused, tested, and explicit about their impact on policy or autonomous repair behavior.

## Before opening a change

- Open an issue for significant architectural, language, or policy changes.
- Include tests for bug fixes and behavior changes.
- Keep security-sensitive behavior fail-closed; do not weaken policy or sandbox restrictions without a reviewed design.
- Do not commit credentials, local `.env` files, binaries, or generated cache data.

## Local checks

From this repository root, run:

```sh
gofmt -w $(find . -name '*.go' -type f)
go vet ./...
go test -race ./...
go build ./cmd/astrix ./cmd/astrixd
```

Docker-backed sandbox checks run when Docker is installed and available. The live LLM test requires an OpenAI-compatible endpoint and credentials in the environment or workspace `.env`.

## Pull requests

Describe the user-visible behavior, security or compatibility impact, and validation performed. Include sample `.ax` changes when modifying configuration behavior. Keep changes to AxLang and Astrix coordinated when their module contracts change.
