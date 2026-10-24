//go:build ignore

// Deterministic synthetic log corpus generator for logpipe tests.
// Run: go run gen.go
package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	outPath := filepath.Join(dir, "app.log")

	rng := rand.New(rand.NewSource(42))
	const total = 20000
	nError := total * 5 / 1000 // 0.5% = 100
	nWarn := total * 35 / 1000 // 3.5% = 700
	nRoutine := total - nWarn - nError

	var lines []string
	lines = append(lines, genRoutine(rng, nRoutine)...)
	lines = append(lines, genWarn(rng, nWarn)...)
	lines = append(lines, genError(rng, nError)...)
	lines = shuffleKeepingBlocks(rng, lines)

	f, err := os.Create(outPath)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	for _, ln := range lines {
		if _, err := fmt.Fprintln(f, ln); err != nil {
			fatal(err)
		}
	}
	fmt.Printf("wrote %s (%d lines)\n", outPath, len(lines))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func genRoutine(rng *rand.Rand, n int) []string {
	shapes := []func(*rand.Rand) string{
		func(r *rand.Rand) string {
			methods := []string{"GET", "GET", "GET", "POST", "PUT", "DELETE"}
			paths := []string{
				"/api/items/%d", "/api/users/%d", "/health", "/ready",
				"/api/orders/%d", "/static/app.js", "/api/search",
			}
			p := paths[r.Intn(len(paths))]
			if strings.Contains(p, "%d") {
				p = fmt.Sprintf(p, r.Intn(10000)+1)
			}
			status := 200
			if r.Intn(10) == 0 {
				status = 304
			}
			return fmt.Sprintf("INFO %s %s %d %dms", methods[r.Intn(len(methods))], p, status, 1+r.Intn(80))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"level":"info","msg":"request","method":"GET","path":"/api/items/%d","status":200,"dur_ms":%d}`, r.Intn(5000)+1, 1+r.Intn(50))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"level":"debug","msg":"db.query","sql":"SELECT * FROM items WHERE id=%d","took_ms":%d}`, r.Intn(9000)+1, 1+r.Intn(20))
		},
		func(r *rand.Rand) string {
			hit := "hit"
			if r.Intn(3) == 0 {
				hit = "miss"
			}
			return fmt.Sprintf("DEBUG cache %s key=%x", hit, r.Uint64())
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("INFO health check ok latency=%dms", 1+r.Intn(5))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("level=info msg=job.tick job=cleanup n=%d", r.Intn(100))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"lvl":"info","event":"session","user_id":"%s","ip":"10.0.%d.%d"}`, uuid(r), r.Intn(255), r.Intn(255))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("I0102 15:04:%02d.000000 1 server.go:%d] handled /api/ping", r.Intn(60), 10+r.Intn(200))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("DEBUG:app.http: status=200 path=/metrics bytes=%d", 100+r.Intn(5000))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("ts=%s level=info msg=render template=home.html took=%dms", time.Unix(1700000000+int64(r.Intn(1e6)), 0).UTC().Format(time.RFC3339), 1+r.Intn(30))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("[INFO] fetched page id=%d size=%dKB", r.Intn(1000), 1+r.Intn(200))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"level":30,"msg":"pino info","reqId":"%s"}`, uuid(r))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("INFO GET /api/cart/%d 200 3ms peer=192.168.1.%d", r.Intn(500), 1+r.Intn(254))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("level=debug msg=sql rows=%d took=%dms", r.Intn(50), 1+r.Intn(15))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("INFO:worker: processed batch=%d ok=true", r.Intn(10000))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"log.level":"info","action":"auth.ok","uid":%d}`, r.Intn(1e6))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("INFO access method=GET path=/assets/logo.png status=200 dur=1ms size=%dB", 500+r.Intn(5000))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("[DEBUG] pool idle=%d active=%d", r.Intn(10), r.Intn(20))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"severity":"INFO","logger":"http","msg":"completed","trace":"%x"}`, r.Uint64())
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("FINE: detail probe=%d", r.Intn(100))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("Jan  2 15:04:%02d app[1]: level=info accepted connection", r.Intn(60))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("INFO starting request id=%s", uuid(r))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"level":"info","msg":"cache.warm","keys":%d}`, 10+r.Intn(90))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("level=info msg=heartbeat seq=%d", r.Intn(1e6))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("INFO GET /api/feed?cursor=%d 200 %dms", r.Intn(1e5), 2+r.Intn(40))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"level":"debug","component":"router","route":"/v1/items/%d"}`, r.Intn(999))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("INFO db.query table=orders id=%d took=%dms", r.Intn(8000), 1+r.Intn(25))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("[INFO] scheduled job next_run=%s", time.Unix(1700000000+int64(r.Intn(86400)), 0).UTC().Format(time.RFC3339))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("level=info msg=ratelimit.ok ip=10.1.%d.%d", r.Intn(255), r.Intn(255))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"@level":"info","event":"payload","bytes":%d}`, 64+r.Intn(4096))
		},
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, shapes[rng.Intn(len(shapes))](rng))
	}
	return out
}

func genWarn(rng *rand.Rand, n int) []string {
	shapes := []func(*rand.Rand) string{
		func(r *rand.Rand) string {
			return fmt.Sprintf("WARN slow query took=%dms sql=SELECT id FROM items WHERE id=%d", 200+r.Intn(800), r.Intn(9000))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"level":"warn","msg":"retry","attempt":%d,"path":"/api/pay"}`, 1+r.Intn(5))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("[WARN] deprecated API /v1/legacy/%d used", r.Intn(100))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("WARNING:app.db: connection pool high usage=%d%%", 70+r.Intn(25))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("level=warn msg=rate.limit ip=192.168.0.%d", 1+r.Intn(254))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("W0315 12:00:%02d.000000 1 db.go:%d] slow statement", r.Intn(60), 40+r.Intn(80))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"severity":"warning","msg":"cache stampede","key":"%x"}`, r.Uint32())
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("WARN timeout upstream=payments latency=%dms", 500+r.Intn(1500))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`{"level":40,"msg":"pino warn","code":"SLOW_IO","ms":%d}`, 100+r.Intn(400))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf("WARNING client disconnect mid-request id=%s", uuid(r))
		},
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, shapes[rng.Intn(len(shapes))](rng))
	}
	return out
}

func genError(rng *rand.Rand, n int) []string {
	var out []string
	for len(out) < n {
		remaining := n - len(out)
		switch rng.Intn(8) {
		case 0:
			out = append(out, fmt.Sprintf(`[ERROR] handler failed id=%d err=timeout`, rng.Intn(10000)))
		case 1:
			out = append(out, fmt.Sprintf(`{"level":"error","msg":"db down","code":%d}`, 500+rng.Intn(20)))
		case 2:
			out = append(out, fmt.Sprintf(`{"level":"fatal","msg":"cannot bind","port":%d}`, 8000+rng.Intn(100)))
		case 3:
			out = append(out, fmt.Sprintf(`E0102 15:04:%02d.0 1 main.go:1] request failed`, rng.Intn(60)))
		case 4:
			block := []string{
				"Traceback (most recent call last):",
				fmt.Sprintf(`  File "/app/server.py", line %d, in handle`, 10+rng.Intn(200)),
				`    return process(req)`,
				fmt.Sprintf(`  File "/app/db.py", line %d, in process`, 5+rng.Intn(80)),
				fmt.Sprintf("ValueError: bad id %d", rng.Intn(1000)),
			}
			if len(block) > remaining {
				out = append(out, fmt.Sprintf("ERROR:root: unhandled exception id=%d", rng.Intn(5000)))
				continue
			}
			out = append(out, block...)
		case 5:
			block := []string{
				`Exception in thread "main" java.lang.RuntimeException: boom`,
				fmt.Sprintf("\tat com.example.App.handle(App.java:%d)", 20+rng.Intn(100)),
				fmt.Sprintf("\tat com.example.App.main(App.java:%d)", 5+rng.Intn(40)),
				"\tat java.base/jdk.internal.reflect.NativeMethodAccessorImpl.invoke0(Native Method)",
			}
			if len(block) > remaining {
				out = append(out, fmt.Sprintf(`[ERROR] java fail id=%d`, rng.Intn(1000)))
				continue
			}
			out = append(out, block...)
		case 6:
			block := []string{
				"panic: runtime error: invalid memory address",
				fmt.Sprintf("goroutine %d [running]:", 1+rng.Intn(20)),
				fmt.Sprintf("main.(*Server).Serve(0x%x)", rng.Uint64()),
				fmt.Sprintf("\t/home/dev/app/server.go:%d +0x%x", 40+rng.Intn(100), rng.Uint32()),
			}
			if len(block) > remaining {
				out = append(out, fmt.Sprintf(`{"level":"error","msg":"panic recovered","n":%d}`, rng.Intn(100)))
				continue
			}
			out = append(out, block...)
		default:
			out = append(out, fmt.Sprintf("ERROR:root: unhandled exception id=%d", rng.Intn(5000)))
		}
	}
	return out[:n]
}

func shuffleKeepingBlocks(rng *rand.Rand, lines []string) []string {
	type block []string
	var blocks []block
	i := 0
	for i < len(lines) {
		ln := lines[i]
		if isBlockStart(ln) {
			b := block{ln}
			i++
			for i < len(lines) && isBlockCont(lines[i]) {
				b = append(b, lines[i])
				i++
			}
			blocks = append(blocks, b)
			continue
		}
		blocks = append(blocks, block{ln})
		i++
	}
	rng.Shuffle(len(blocks), func(i, j int) { blocks[i], blocks[j] = blocks[j], blocks[i] })
	out := make([]string, 0, len(lines))
	for _, b := range blocks {
		out = append(out, b...)
	}
	return out
}

func isBlockStart(s string) bool {
	return strings.HasPrefix(s, "Traceback (most recent call last)") ||
		strings.HasPrefix(s, "Exception in thread") ||
		strings.HasPrefix(s, "panic:")
}

func isBlockCont(s string) bool {
	return strings.HasPrefix(s, "  File ") ||
		strings.HasPrefix(s, "    ") ||
		strings.HasPrefix(s, "\tat ") ||
		strings.HasPrefix(s, "goroutine ") ||
		strings.HasPrefix(s, "main.") ||
		strings.HasPrefix(s, "\t/") ||
		strings.HasPrefix(s, "ValueError:")
}

func uuid(r *rand.Rand) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", r.Uint32(), r.Intn(0xffff), r.Intn(0xffff), r.Intn(0xffff), r.Uint64()&0xffffffffffff)
}
