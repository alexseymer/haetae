# Summarising logs

Reading logs is slow. Haetae can write you a short summary of the warnings and errors from one host or from all hosts, and say what is new and what got worse.

## What you get

A log summary looks at a time window of 1 hour, 6 hours or 24 hours and compares it with the window of equal length just before. It reports error patterns that appeared for the first time in this window, patterns that spiked (at least ten lines, and at least three times as many as before), and what is worth looking at first. Each statement carries links to the evidence behind it. Identical log lines are merged into one pattern with a count, so a thousand repeats of the same error are one item.

In our example: `shop-api` starts logging "connection refused" to `shop-db` at 03:12. The 24-hour summary says this pattern is new, gives its count and links to the lines, instead of leaving you to scroll through the raw log.

## How to ask for it

In a linked chat channel, send the command `/logs`. Without anything else it covers every host for the last 24 hours. You can add a host name and a window in either order:

```
/logs
/logs hel1-prod
/logs hel1-prod 6h
/logs 1h
```

The window must be `1h`, `6h` or `24h`. Any other value gets a reply saying so. An unknown host name gets a reply listing the hosts you have.

## The daily digest

Some time after the briefing each day (five minutes after the briefing hour, in your time zone), Haetae can post one message that combines the 24-hour log summary and the [vulnerability digest](./watching-vulnerabilities). It posts only on a day when there is something to say: new error patterns, a spike, or new critical or high vulnerabilities, or a scan that did not complete. On a calm day you get nothing. It is switched on by default and there is no switch for it in the interface yet.

## With and without the AI

When the operator is on and reachable, an AI writes the summary, and any statement without evidence is discarded. When it is off, over its daily cap, or the answer does not pass the checks, Haetae falls back to a plain text built from the same data and tells you why, for example "(rule-based: ...)". The data and the evidence links are the same either way.

Warnings, errors and fatal lines are never left out of the data, and the summary never tells you that everything is fine; it only summarises what it has.
