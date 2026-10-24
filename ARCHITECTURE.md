# Astrix architecture

## Analysis path

```text
unified diff
  → ingress parsing + secret scan
  → Go / Tree-sitter language parsers
  → normalized SymbolNode / DefEdge / RefEdge graph
  → modified-line definition selection
  → reverse-reference transitive closure
  → impacted symbols and tests
```

The graph layer is language-neutral. Source adapters create symbol nodes with file/line metadata and definition/reference edges; the blast-radius engine traverses graph references without depending on a language parser. Go uses the standard library parser; TypeScript and Python use Tree-sitter.

## Runtime subsystems

- **Cache:** stable SHA-256 subgraph keys address local verification outcomes beneath `.astrix/cache`.
- **Quarantine:** SQLite stores a rolling set of outcomes per test and source identity; transition history yields the failure fraction used by quarantine decisions.
- **Policy:** AxLang-generated Rego v1 modules are evaluated against JSON `ActionIntent` objects. Credential scanning occurs before autonomous processing.
- **Repair:** logs are normalized, impact context is assembled into a model request, and returned unified diffs pass bounded-attempt and policy checks before test execution in a network-disabled Docker sandbox.
- **MCP:** the stdio JSON-RPC server exposes blast-radius, policy-evaluation, and impacted-test operations.
- **Canary:** AxLang deploy declarations are converted into traffic steps, polling cadence, and metric limits. Prometheus and traffic-controller interfaces keep monitoring and deployment systems replaceable.

## Executables

`cmd/astrix` provides local check, graph, verify, repair, and MCP commands. `cmd/astrixd` receives authenticated webhook requests and performs ingress and policy checks. The library packages are also usable independently by Go applications.

## Security boundaries

The repair loop has a hard maximum of three attempts, checks patch paths and policy before application, and validates candidate changes without Docker network access. `astrixd` authenticates webhook requests with HMAC-SHA256 but does not provide TLS termination; operate it behind a trusted TLS-terminating proxy.
