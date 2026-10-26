# Security UI (M7)

Host page **Security** tab (`?tab=security`) shows vulnerabilities, misconfigurations, outbound connections, and image update decisions. The hosts list shows a small badge (critical+high count, or "scan failed") linking to that tab.

## API (session cookie; CSRF header on writes; members read, admins write)

| Method | Path | Notes |
|--------|------|--------|
| GET | `/api/hosts/{id}/security` | Conntrack state, open misconfigs, severity summary; cross-org → 404 |
| GET | `/api/hosts/{id}/vulnerabilities` | Per-container scan/match status, counts, top 200 findings (https URLs only) |
| POST | `/api/hosts/{id}/containers/{cid}/scan` | Admin; 202 forced `ScanContainer`; one per container per minute → 429 |
| GET | `/api/hosts/{id}/outbound` | Aggregated outbound rows (`?limit=`) |
| GET | `/api/hosts/{id}/image-updates` | Update decisions |
| GET/PUT | `/api/settings/updates` | `{ autoPull }`; PUT admin only |

## Truths the UI must keep

- Failed / timeout / oom / unsupported scan, or failed match → **"Scan failed: …"**, never a clean zero-vulnerability presentation.
- Never scanned → status `none` → **"Not scanned yet"**.
- Conntrack degraded/unavailable → banner with the reason; host-network containers listed as **not monitored**.
- CVE links only when the server returned an **https** URL (same allowlist helper as deploy URLs).
- Polling every 15 s while the tab is visible, with the usual stale-response generation guard.
