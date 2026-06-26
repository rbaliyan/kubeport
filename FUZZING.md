# Fuzzing

kubeport uses Go's native fuzzing (`testing.F`) for the parsing, validation,
template-expansion, and address-translation surfaces that handle untrusted or
user-controlled input. Targets run continuously in CI via
[ClusterFuzzLite](https://google.github.io/clusterfuzzlite/): a per-PR
code-change run (`.github/workflows/cflite_pr.yml`) and a monthly batch run
(`.github/workflows/cflite_batch.yml`).

## Targets

| Target | Package | What it checks |
|--------|---------|----------------|
| `FuzzUnmarshalYAML` | `pkg/config` | YAML decode + round-trip idempotence; drives `ResolveNetwork`/`ResolveChaos`/`Parse` |
| `FuzzUnmarshalTOML` | `pkg/config` | TOML decode (`parsePortsFromRaw`) + round-trip idempotence |
| `FuzzValidateService` | `pkg/config` | `ValidateService` never panics on arbitrary service configs |
| `FuzzLoadWithInheritance` | `pkg/config` | `Load` → `loadWithInheritance`/`mergeConfigs`/extends cycle detection |
| `FuzzExpandVars` | `internal/hook` | **Security:** shell metacharacters are stripped from user fields before substitution |
| `FuzzExpandVarsJSON` | `internal/hook` | `ExpandVarsJSON` output embedded in a JSON document stays `json.Valid` |
| `FuzzParseEventType` | `internal/hook` | `ParseEventType` never panics |
| `FuzzBuildFromConfig` | `internal/hook` | `BuildFromConfig` never panics on arbitrary hook configs |
| `FuzzParseSvcFlag` | `internal/cli` | A successful `--svc` parse only fails validation for the two deferred checks (name, port range) |
| `FuzzServiceConfigProtoRoundTrip` | `internal/cli` | `serviceConfigToProto` preserves the semantically meaningful fields on round-trip |
| `FuzzResolveAddr` | `pkg/proxy` | Address translation returns the input or a mapping value — never garbage |
| `FuzzHTTPCheckAuth` | `pkg/proxy` | `checkAuth` tolerates any `Proxy-Authorization` header and only accepts matching credentials |

## Running

Run one target for a fixed time:

```bash
just fuzz FuzzUnmarshalYAML        # 30s default
just fuzz FuzzExpandVars 2m
# equivalently:
go test -run='^$' -fuzz='^FuzzExpandVars$' -fuzztime=30s ./internal/hook/
```

Run every target briefly in sequence (a fuzzing smoke pass):

```bash
just fuzz-all          # 15s each
just fuzz-all 1m
```

The normal test run also executes every committed seed corpus once (no
mutation), so `go test ./...` exercises all targets against their seeds.

## Reproducing a crash

When a target finds a failing input, Go writes it to
`testdata/fuzz/<FuzzName>/<hash>` under the target's package. Re-run just that
input:

```bash
go test -run='FuzzExpandVars/<hash>' ./internal/hook/
```

A reported oracle violation is one of two things:

1. A **real bug** in production code — escalate it; do not delete the assertion.
2. An **oracle that overstates the contract** — correct the oracle to match the
   code's documented guarantee and explain why in the test comment.

## Refreshing the seed corpus

Seed corpora live under each package's `testdata/fuzz/<FuzzName>/`. To add a
new seed, either add an `f.Add(...)` call in the target or drop a Go corpus
file in that directory (format: a `go test fuzz v1` header line followed by one
typed argument per line, e.g. `[]byte("...")`, `string("...")`, `int(5)`,
`bool(true)`). Interesting inputs the fuzzer discovers are cached in `$GOCACHE`;
promote a useful one by copying it into `testdata/fuzz/<FuzzName>/`.

## Adding a target

After adding a `func Fuzz*`, register it in `.clusterfuzzlite/build.sh` with a
`compile_native_go_fuzzer` line. The drift-guard test
(`TestFuzzTargetsRegisteredInClusterFuzzLite`) fails if any target is missing
from the build script.
