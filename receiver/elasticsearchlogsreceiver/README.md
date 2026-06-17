# Elasticsearch Logs Receiver

| Status        |               |
| ------------- |---------------|
| Stability     | [development]: logs |
| Distributions | [contrib]     |

This receiver queries an Elasticsearch [`_search`](https://www.elastic.co/guide/en/elasticsearch/reference/current/search-search.html)
endpoint on a fixed interval and emits the matching documents as logs.

It paginates through results using [`search_after`](https://www.elastic.co/guide/en/elasticsearch/reference/current/paginate-search-results.html#search-after)
combined with a stable sort, which is more efficient and reliable than deep `from`/`size` paging.
When a [storage extension](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/extension/storage)
is configured, the receiver persists its `search_after` cursor so that, after a collector restart,
it resumes from where it stopped instead of re-reading or skipping documents.

## How it works

On each poll cycle the receiver issues:

```json
POST /<indices>/_search
{
  "size": 1000,
  "search_after": ["2026-06-17T10:15:23.123Z", "abc123"],
  "sort": [
    { "@timestamp": "asc" },
    { "event.id": "asc" }
  ]
}
```

It keeps requesting pages, advancing `search_after` to the `sort` values of the last document of each
page, until a page returns fewer hits than `page_size` (meaning it has caught up). The cursor is
persisted only **after** a page has been successfully passed to the next consumer, giving
at-least-once delivery: a crash mid-cycle resumes from the last acknowledged page rather than losing
data.

> **Note**: `search_after` requires a deterministic sort. The **last** entry in `sort` must be a field
> that is unique per document (for example a document id or `event.id`); otherwise documents sharing the
> same sort values can be skipped or duplicated. At least one sort field is required; a unique tiebreaker
> as the last entry is strongly recommended.

## Configuration

| Field | Default | Description |
|-------|---------|-------------|
| `endpoint` | `http://localhost:9200` | Base URL of the Elasticsearch cluster. |
| `username` / `password` | | HTTP basic auth credentials. Must be set together. |
| `api_key` | | Elasticsearch API key, sent as `Authorization: ApiKey <key>`. Mutually exclusive with basic auth. |
| `tls` | | TLS client settings (see [confighttp]/[configtls]). |
| `indices` | _(required)_ | List of index or data-stream patterns to search, e.g. `["logs-*"]`. |
| `query` | | Optional raw Elasticsearch query DSL object. ANDed with the initial time-range filter. |
| `timestamp_field` | `@timestamp` | Document field used as the time cursor and for the initial range filter. |
| `sort` | _(required)_ | Stable sort. Each entry maps one field to `asc`/`desc`; at least one field is required, and the last entry should be unique per document. |
| `page_size` | `1000` | Number of documents per `_search` request (the query `size`). |
| `poll_interval` | `30s` | How often a new search cycle starts. |
| `initial_delay` | `1s` | Delay before the first poll after startup. |
| `start_at` | `end` | Where to begin on a fresh start (no checkpoint): `beginning` reads all history, `end` reads only recent documents. |
| `initial_lookback` | `0` | When `start_at: end`, how far back from "now" to begin. `0` means only documents ingested after startup. |
| `storage` | | ID of a storage extension used to persist the `search_after` cursor across restarts. If unset, the cursor is in-memory only. |

The connection settings (`endpoint`, `tls`, timeouts, etc.) are the standard [confighttp] client
options.

## Emitted logs

Each matching document becomes one log record:

- **Body**: the full `_source` document, as a structured map.
- **Timestamp**: parsed from `timestamp_field`.
- **ObservedTimestamp**: time the document was read.
- **Attributes**: `elasticsearch.index` (the hit's `_index`) and `elasticsearch.id` (the hit's `_id`).

## Example

```yaml
extensions:
  file_storage:
    directory: /var/lib/otelcol/storage

receivers:
  elasticsearchlogs:
    endpoint: https://elasticsearch:9200
    username: otel
    password: ${env:ES_PASSWORD}
    indices:
      - logs-*
    query:
      bool:
        must:
          - term:
              service.name: checkout
    timestamp_field: "@timestamp"
    sort:
      - "@timestamp": asc
      - "event.id": asc
    page_size: 1000
    poll_interval: 30s
    start_at: end
    initial_lookback: 1h
    storage: file_storage

service:
  extensions: [file_storage]
  pipelines:
    logs:
      receivers: [elasticsearchlogs]
      exporters: [debug]
```

[development]: https://github.com/open-telemetry/opentelemetry-collector#development
[contrib]: https://github.com/open-telemetry/opentelemetry-collector-releases/tree/main/distributions/otelcol-contrib
[confighttp]: https://github.com/open-telemetry/opentelemetry-collector/blob/main/config/confighttp/README.md
[configtls]: https://github.com/open-telemetry/opentelemetry-collector/blob/main/config/configtls/README.md
