# Development deployment samples

The files in this directory are for **local development and testing only**.
They are not production deployment instructions.

For production deployment documentation, see [Self-hosting](../README.md#self-hosting)
in the main README.

## Local samples

- `docker-compose.yml`: Cronpad with OTel Collector, Prometheus, and Grafana
  for local development. Cronpad uses SQLite without Litestream replication;
  the monitoring services are development tooling, not app dependencies.
- `docker-compose.seaweedfs.yml`: a standalone backup demo with Cronpad,
  SeaweedFS (S3-compatible storage), and bucket initialization.

Run the local development stack from the repository root:

```sh
export CRONPAD_AUTH__DEV_LOGIN=true
export CRONPAD_SECRETS__ENCRYPTION_KEY="$(openssl rand -base64 32)"
docker compose -f deploy/docker-compose.yml up --build
```

Open <http://localhost:8080/>. Keep the encryption key if you want to reuse
stored secrets.

## Local monitoring

All monitoring services start by default with `docker-compose.yml`:

- [Grafana](http://localhost:3000/): log in with `admin` / `admin` on a fresh
  instance, then open the **Cronpad dispatch** dashboard.
- [Prometheus](http://localhost:9090/): queries and the sample alert rules.
- OTel Collector: receives Cronpad metrics, traces, and logs over OTLP. Metrics
  are scraped by Prometheus; traces and logs go to the collector's console,
  not Grafana. View them with
  `docker compose -f deploy/docker-compose.yml logs -f otel-collector`.

Monitoring ports are bound to localhost. No trace or log storage backend is
bundled, and monitoring data is not persisted across container recreation.

## Backup demo

To try the backup demo instead:

```sh
docker compose --env-file deploy/demo.env \
  -f deploy/docker-compose.seaweedfs.yml up --build
```

The demo uses fixed credentials and an encryption key from `demo.env`.
**Do not use these values in production**, and do not reuse an existing data
volume encrypted with a different key. Development login must never be enabled
on a publicly reachable deployment.
