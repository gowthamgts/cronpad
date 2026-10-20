# cronpad

A durable scheduler for webhooks and commands. One Go binary serves the API,
dashboard and MCP server; two SQLite files keep durable state, and a third,
rebuildable SQLite file is the delivery queue. Nothing else needs to run. Runs use
at-least-once delivery, so receivers should be idempotent.

To try it locally with development login:

```sh
export CRONPAD_AUTH__DEV_LOGIN=true CRONPAD_SECRETS__ENCRYPTION_KEY="$(openssl rand -base64 32)"
docker compose -f deploy/docker-compose.yml up --build
# Open http://localhost:8080/. Keep the key: stored secrets can't be decrypted without it.
```

Data lives in the `cronpad-data` volume. Nothing is backed up until you point
Litestream at object storage; see [backups](https://cronpad.com/docs/self-hosting/backups).

For development, install Go, Node, pnpm and just, then run `just dev`. Use `just check` before contributing. Release tools are version-pinned
in separate Go tool modules; `just release` builds archives, checksums and SBOMs.

See [installation](https://cronpad.com/docs/self-hosting/installation),
[production checklist](https://cronpad.com/docs/self-hosting/production-checklist),
[backups](https://cronpad.com/docs/self-hosting/backups), [storage](https://cronpad.com/docs/concepts/storage),
and [contributing](CONTRIBUTING.md). `just loadtest` measures the real API and
webhook pipeline against a local echo server.

Measured on an Apple M5 Pro (15 CPUs, 24 GiB RAM), using the real API and local
HTTP deliveries for 60 seconds in durable mode, with 256 loadgen workers and the
default group-commit result writer (the shipped worker-count default is 64):

| Mode | CPU budget | Successful runs/min | Start lag p50 / p95 / p99 |
|---|---|---:|---|
| Durable (default) | Default (15) | 12,000 | 37 / 49 / 54 ms |
| Durable (default) | 2 CPUs (`GOMAXPROCS=2`) | 12,000 | 43 / 58 / 60 ms |
| Fast (opt-in) | Default (15) | 12,000 | 28 / 37 / 39 ms |
| Fast (opt-in) | 2 CPUs (`GOMAXPROCS=2`) | 12,000 | 21 / 35 / 40 ms |
| Durable (default) | Default (15) | 30,000 | 67 / 115 / 139 ms |

The 12,000/min rows are the median of three 60-second runs each (each percentile
taken separately); single runs varied by up to ±10 ms, and one two-CPU durable
run reached p99 89 ms.

SQLite + WAL (all three files) grew about 11 MB per 12,000 runs. A single job
firing once a second starts in 2–4 ms.

Every run recorded all successes and zero duplicates. Fast mode starts the
request before the admission commit lands, so a crash can repeat a delivery:
receivers must deduplicate by idempotency key.

At higher offered load (30-second runs, every job due in the same instant) the
same machine delivered all runs with zero duplicates: p99 111 ms at 30,000/min,
211 ms at 60,000/min and 393 ms at 120,000/min; spread across each second, p99
stayed at 2 ms.

These are local measurements, not a claim about a two-vCPU cloud VM or network
disks. See [storage](https://cronpad.com/docs/concepts/storage) for workload
details, durable vs fast mode and the janitor comparison.

## License

cronpad is licensed under AGPL-3.0; the full license is in [LICENSE](LICENSE).
Contributions are covered by the [CLA](CLA.md).

Self-hosting is free. The hosted service at [cronpad.com](https://cronpad.com) and
enterprise features are offered separately by the project owner.
