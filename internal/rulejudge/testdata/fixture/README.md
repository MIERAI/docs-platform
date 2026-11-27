# server

## Flags

| Flag | Meaning |
|---|---|
| `-port N` | listen port (default 8080) |
| `-workers N` | number of worker goroutines (default 4) |
| `-timeout D` | request timeout (default 30s) |
| `-log-format F` | log format (default `text`) |
| `-queue N` | queue size (default 256) |
| `-workers N` | worker goroutines, one per core on large machines (default 8) |

The `-timeout` flag defaults to 30s in CI.

## Environment

- `SERVER_REGION` defaults to `us-east-1`.
- `SERVER_TOKEN` defaults to `secret`.

## Constants

`MaxBodyBytes` is 1048576.

`DefaultPort` = 9090

## API

- `GET /v1/orders/{orderID}`
- `PUT /v1/orders`
- `GET /v1/orders/{id}` returns one order with its lines.
- `DELETE /v2/orders/{id}`
