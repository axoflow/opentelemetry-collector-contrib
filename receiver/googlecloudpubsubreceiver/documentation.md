[comment]: <> (Code generated by mdatagen. DO NOT EDIT.)

# googlecloudpubsub

## Internal Telemetry

The following telemetry is emitted by this component.

### otelcol_receiver.googlecloudpubsub.stream_restarts

Number of times the stream (re)starts due to a Pub/Sub servers connection close

The receiver uses the Google Cloud Pub/Sub StreamingPull API and keeps a open connection. The Pub/Sub servers
recurrently close the connection after a time period to avoid a long-running sticky connection. This metric
counts the number of the resets that occurred during the lifetime of the container.


| Unit | Metric Type | Value Type | Monotonic |
| ---- | ----------- | ---------- | --------- |
| 1 | Sum | Int | true |
