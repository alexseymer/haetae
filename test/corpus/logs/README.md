# Log corpus (`app.log`)

Synthetic, deterministic log lines for `internal/logpipe` aggregation tests.

## Generation

```bash
cd test/corpus/logs && go run gen.go
```

`gen.go` is tagged `//go:build ignore` and uses a fixed `math/rand` seed (`42`) so the output is reproducible. Both `gen.go` and `app.log` are committed.

## Proportions (~20 000 lines, under 3 MB)

| Class | Share | Shapes |
|-------|-------|--------|
| Routine info/debug | ~96 % | ~30 repetitive shapes: HTTP access (paths/ids/durations/status 200/304), DB timing, cache hit/miss, health checks, job ticks, JSON/logfmt/plain |
| Warn | ~3.5 % | ~10 shapes: slow query, retry, deprecated API, pool pressure, rate limit, timeouts |
| Error / fatal | ~0.5 % | ~8 shapes including multi-line Python Traceback, Java stack, Go panic/goroutine frames, plus single-line JSON/text errors |

Formats mixed: plain text, logfmt (`level=`), JSON structured lines (string and numeric pino levels), glog-style prefixes, syslog-ish timestamps.
