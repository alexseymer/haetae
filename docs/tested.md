# What is tested and what is not

We would rather tell you the limits before you install than have you find them. This page says plainly what has been checked and what has not.

## What has been tested

Haetae has an automated test suite and a longer acceptance run that checks each of its promises: a host comes online without an open port, a failed deploy leaves the old version running, a serious alert reaches chat quickly, the operator cites evidence or says "cause not found", secrets are removed before a cloud AI sees anything, the agent stays under its memory goal, and a failed scan is never shown as clean. All of these passed. The full run takes about forty minutes and its detailed report is kept in the project for contributors.

A few numbers from that run, so you have a feeling for it: a host showed as online 116 milliseconds after enrolling, a deploy with cached layers was healthy in about 6 seconds, the agent used about 29 MiB at its peak with 20 noisy containers, and a fresh install reached a working login in about 31 seconds once the image was built.

## What the tests do not show

Everything ran on a single test machine, with two processor cores and 8 GB of memory. The "hosts" were Docker containers on that same machine, so all timings are for a connection on the same computer. They show that Haetae itself is not the slow part. They do not tell you what happens over a real network or with a fleet of servers.

The AI was a scripted stand-in. It was used to check the rules around the AI: evidence, redaction, autonomy levels and limits. The connection formats for Anthropic and for OpenAI-compatible services were tried against test servers. **Haetae has not yet been run with a real AI provider**, so how good its answers are with a real model is not measured. The same goes for the log summaries and the vulnerability digest. The chat question path has also shown that some questions about containers are refused by the provider's safety filter, so do not rely on container questions always working.

Chat services were not tried live. Telegram was tested against a fake Telegram server. Slack, Signal and email were covered only by tests of the code that talks to them. Expect small details to need fixing the first time, such as which permissions your Slack app needs.

Certificates from a public authority such as Let's Encrypt were not requested; the tests used Caddy's internal authority. Only the 64-bit Intel and AMD version of the agent was actually run. The ARM version was checked only by looking at the file header.

The vulnerability checks that raise alerts were tested with a made-up database. The real database was used for measuring memory and for comparing with another scanner. Large images can fail their scan on a small server, as described in [Problems](./problems/#a-scan-fails).

## Release and operations

There is no published container image yet. You build the server from source, as described in [Install the server](./getting-started/install-the-server). There are no signed releases and no image for several processor types. The hosted service is planned but not built. An agent's certificate lasts a year and cannot yet be renewed, so you have to enroll a host again before the year ends.
