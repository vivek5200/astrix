Output "docs/assets/astrix-demo.gif"
Set Shell "bash"
Set FontSize 14
Set Width 100
Set Height 30
Set Padding 16
Set Theme "Catppuccin Mocha"
Hide
Type "printf '\\033[32m$\\033[0m axc check Astrixfile.ax\\n'"
Enter
Sleep 250ms
Type "printf '\\033[32m✓\\033[0m Astrixfile.ax: valid (compiled 2 OPA Rego guardrails, 1 verify stage, 1 heal loop)\\n'"
Enter
Sleep 1750ms
Type "printf '\\033[36m$\\033[0m astrix graph --diff\\n'"
Enter
Sleep 250ms
Type "printf '[change] pkg/calc/calc.go::Multiply modified (line 3)\\n[blast-radius] reverse-DAG selected pkg/calc/calc_test.go::TestMultiply\\n[scope] skipped 14 unrelated packages\\n'"
Enter
Sleep 2250ms
Type "printf '\\033[36m$\\033[0m astrix verify --fix\\n'"
Enter
Sleep 250ms
Type "printf '\\033[32m[ingress] secret scan clean (0 leaks)\\n\\033[0m[sandbox] docker run --network=none golang:1.23-alpine go test ./pkg/calc\\n\\033[31m✗ FAIL: TestMultiply: Multiply(6, 7) = 13, want 42\\033[0m\\n'"
Enter
Sleep 2750ms
Type "printf '[triage] attempt 1/3: querying qwen-2.5-coder-32b (temp=0)...\\n\\033[31m- func Multiply(a, b int) int { return a + b }\\033[0m\\n\\033[32m+ func Multiply(a, b int) int { return a * b }\\033[0m\\n[policy] OPA guardrail \\\"security_boundaries\\\": ALLOW (tests untouched, protected paths clean)\\n'"
Enter
Sleep 2750ms
Type "printf '[sandbox] re-verifying patch in isolated --network=none container...\\n\\033[32m✓ PASS: pkg/calc (0.42s) — Merkle hash 6a38f499 cached\\n\\033[36m⚡ Self-healing complete in 4.53s (attempt 1/3)\\033[0m\\n'"
Enter
Sleep 3500ms
