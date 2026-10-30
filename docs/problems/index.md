# Problems and what to do

Start with the symptom you see. Messages in quotation marks are the real texts Haetae shows. Two places help in almost every case: the server log, with `docker compose --project-directory /opt/haetae logs server` (add `-f` to follow it), and the agent log on a host, with `journalctl -u haetae-agent`.

## A host shows offline

Suppose hel1-prod suddenly shows **Offline**. Haetae marks a host offline when it has not been heard from for the **Offline after seconds** time (default 120) and raises a critical alert.

First look at the machine. Is the agent running? `systemctl status haetae-agent` tells you. If the installer ended with "haetae-agent failed to become active; see: journalctl -u haetae-agent", read that journal. Next check that the folder `/var/lib/haetae-agent` contains `identity.json`; without it the host never enrolled.

If enrolling itself failed, the messages tell you why. "enrollment token is invalid, expired or already used" means the token was spent or too old: create a new one under **Hosts**, **Add host**. "server returned a CA that does not match the pin" means the command does not belong to this server, so copy it again from the interface. "agent: already enrolled" means the folder already holds an identity; delete it only if you really want to enroll again.

Then check the network. The host must reach your Haetae server's gateway outbound, by default `https://<HAETAE_HOST>:8443`. From the host, `curl -vk https://<server>:8443` should connect. If you installed with a private address that the host cannot reach, set a reachable name as `HAETAE_HOST`, run the installer again and re-enroll.

The agent log says what is going on. A line `connected` means the session is up. `session ended, reconnecting` shows why it dropped and that it will try again, waiting between one and thirty seconds. "host revoked" means someone removed the host. Finally, the agent needs Docker: its user must be in the `docker` group, and the service must be restarted after you add it.

## A deploy fails

Open the project and select the deploy. The result panel shows the raw error and the log viewer shows the build log. The status tells you where it went wrong. **Failed** means fetching or building failed and the running version was not touched. **Rolled back** means the new version did not start and the old one was restored. **Rollback failed** means even that did not work and you need to look at the host.

Common messages: "deploys: host is not connected" means the host is offline, so fix that first. "deploys: a deploy is already running for this project" means wait. Errors from `git fetch` or `git checkout` usually mean the address must be plain `https://`, a private repository needs an **Access token**, or the **Ref** does not exist. `service "<name>" not found in compose ps` or "ingress not available" means the **Service** field does not match a service in the compose file. "domain requires service and port" means the three domain fields must be filled together. Messages like `service api state restarting` or `health unhealthy` mean the container did not stay up during the ten-second check, so read its logs on the host's **Containers** or **Logs** tab. A start longer than 3 minutes or a build longer than 15 is cut off. Lines shown as withheld are output that could not be cleaned of secrets, so it was not sent.

## No HTTPS on a deployed app

Check that the project has **Domain**, **Service** and **Port** all set and that the deploy is Healthy. The domain must point to the host and ports 80 and 443 must be free and reachable from the internet. On the host, `docker ps` should show `haetae-caddy`, and `docker logs haetae-caddy` says what the certificate request did. If another program holds port 80 or 443, Caddy cannot start. Certificates from a public authority have not been tested by the project, so this is the likeliest place for trouble on a real domain.

The browser warning on the Haetae web interface itself is something else: it is expected as long as you use the server's own certificate. See [Install the server](../getting-started/install-the-server).

## A scan fails

On the **Security** tab, "Scan failed" always comes with a reason, and it is never shown as clean. `inventory out of memory` means the image needs more than the 256 MiB the scanning helper may use; this happens with big images such as `node` and `python`. Raise `HAETAE_INVENTORY_MEM` for the agent, for example `Environment=HAETAE_INVENTORY_MEM=640M` in a systemd override, and also raise the unit's `MemoryMax`. `inventory timed out` means the scan took longer than ten minutes, which you can change with `HAETAE_INVENTORY_TIMEOUT`. "too many packages" means the image has more than 20,000.

"Vulnerability database not configured" means `HAETAE_VULN_DB_URL` is empty or `off` in `haetae.env`. "Vulnerability database update failed" means the download failed, and Haetae keeps using the old one; check the server log for `vuln db refresh failed`. **Matching pending** stays until the database exists and the queue is processed. An administrator can press **Scan now** to retry.

## The operator says "cause not found"

That is Haetae being honest: the operator found no evidence for a cause. It can mean the problem is outside what Haetae sees, or the question was too vague. Try asking about a specific container and time. If it says this to every question, the model probably does not support tool calling; **Test connection** in **Settings**, **AI operator** shows "tool calling: not used by this model". Choose a different model.

## The operator says unavailable

The reply reads "The operator is unavailable (reason); rule alerts are active." and the reason names the cause. Your alerts, incidents and briefing are not affected.

`not configured` means the server has no AI settings yet. `turned off` means the box **Enable the AI operator** is not ticked. `daily usage cap reached` means today's allowance is used up; raise the **Daily token cap** or wait for the next UTC day. `the provider rejected the API key (HTTP 401)` means a wrong or expired key. A message about rejecting the request and checking `HAETAE_AI_MODEL` and `HAETAE_AI_BASE_URL` means an unknown model or a wrong address; use the model name exactly as the provider lists it. `the provider is rate limiting requests` means wait a minute. `provider unreachable` means the server cannot reach the service: check the address, DNS and firewall. The provider's safety filter can also refuse a question, and you are advised to rephrase it. "redaction failed; nothing was sent" is deliberate: if cleaning fails, nothing is sent to a cloud provider.

**Test connection** on the AI operator settings page is the quickest way to see the actual reason.

## Chat does not respond

Open **Settings**, **Chat channels** and check that the channel is `enabled`. A channel that needs two settings, such as Slack, stays off if one is missing; the server log at start says `chat service started` with the list of channels. Check that your account is linked under **Linked accounts**, because alerts go only to linked accounts, and link codes last just ten minutes. Remember that only high and critical alerts are sent right away; the others wait for the briefing, and muted alerts are not sent at all.

If a button gets "Action not allowed.", only a linked administrator can press it. If email does not arrive, check the mail settings and the server log. In Slack, linking needs `link CODE` as a direct message without a slash, and Signal needs your own `signal-cli`. Delivery errors appear in the server log as `chat delivery failed`, and delivery is tried up to eight times.

## Memory is high

For the agent the goal is under 50 MB. Check with `systemctl status haetae-agent` or `ps -o rss,cmd -C haetae-agent`. A short rise while a scan runs is a separate helper program, capped at 256 MiB, so on a very small host big images fail their scan instead of using all memory. If the agent restarts again and again, `journalctl -u haetae-agent` shows the `fatal` error.

For the server, large package lists are matched two at a time. Look at `docker stats`; the database is the other container.

If it is your own app that is using memory, that is a normal alert: a container that runs out of memory raises a high-severity alert, and the operator can help you find out why.

## The server does not start

The log tells you. Typical messages: `HAETAE_DATABASE_URL is required`; `HAETAE_WEB_TLS_CERT and HAETAE_WEB_TLS_KEY must be set together`; `HAETAE_TRUST_PROXY` was replaced by `HAETAE_TRUSTED_PROXIES`; `HAETAE_PUBLIC_URL must be an absolute URL`; and for the AI, anything beginning `HAETAE_AI_*:`. The installer itself reports "port N is already in use" or "timed out waiting for the server to become healthy", and then prints the logs.
