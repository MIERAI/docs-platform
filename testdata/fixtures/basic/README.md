# Tool

Run `tool check` to verify. Flags: `--verbose`, `--colour` (renamed), `-n`.

Set `TOOL_TOKEN` (required) and `TOOL_OLD` (removed).

See the [guide](docs/guide.md#install), the [missing page](docs/missing.md) and
the [bad anchor](docs/guide.md#no-such-heading).

Scripts: `scripts/run.sh` and `scripts/gone.sh`; outputs go to `out/report.json`.

Metrics: `tool_requests_total` and `tool_errors_total`; also `node_cpu_seconds_total` (exporter).

```sh
make build
make deploy
tool serve --verbose --port 8080
kubectl apply --server-side -f chart/
./scripts/run.sh -m fast
```

Personal notes: `~/notes/tool.md`.

<!-- docsp:ignore legacy name kept for one release -->
Old flag: `--legacy-mode`.

Config: `replicaCount`, `image.tag`, `image.digest`.

Workflow input `version` of the release workflow.
