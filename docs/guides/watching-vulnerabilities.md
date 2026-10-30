# Watching vulnerabilities

Haetae looks inside every running container and tells you which known security problems (CVEs) affect the software in it. You find the results on the host page under the **Security** tab. On the Hosts list a badge shows the number of critical plus high findings. Members can read the results; administrators can force a scan and change the update setting.

## How a scan works

For each running image, the agent lists the installed packages with their versions. It does this when a container starts, after a deploy turns healthy, once a day, and whenever an administrator presses **Scan now**. Only the list of packages leaves the host, never the contents of the image. The server compares that list with a public vulnerability database. For each finding you see the CVE, its severity, the package, the version you have and the version that fixes it.

The server checks for a new database every ten minutes and matches everything again when there is one. When a new problem affecting a running container appears, you are alerted within about 15 minutes. Alerts are raised for high and critical findings that are new since the container started; the problems already present at first evaluation are shown but not alerted.

To keep the database from downloading, set `HAETAE_VULN_DB_URL=off` in `haetae.env`. The Security tab then says the vulnerability database is not configured.

## Reading the results

The most important rule: Haetae never turns a failure into "no vulnerabilities". You will see one of these states:

**Scan failed** with a reason means the inventory failed, timed out or ran out of memory. It is not clean, and the Hosts list shows `scan failed`. **Not scanned yet** and **Scan pending** are what they say. **Matching pending** means the package list is stored but not yet compared, perhaps because the database is not ready; this is not clean either, and it is retried. Only a line such as "Scanned ... (database ...)" with counts is a real result.

Large images are the usual reason for a failed scan. The helper that lists packages may use 256 MiB; an image such as `node:22-bookworm` needs more, and `alpine` needs much less. On a small server expect big images to end as scan failed. The fix is in [A scan fails](../problems/#a-scan-fails).

## More on the tab

Besides vulnerabilities, the tab reports risky settings of your containers: published ports (database ports rank higher), the Docker socket mounted into a container (critical), running as root, privileged mode, host network, host process view, dangerous capabilities, and certificates that are about to expire. It also lists outbound connections per container, grouped by destination, so you can notice that `shop-web` suddenly talks to an address it never used. That needs the `nf_conntrack` module on the host. If it is missing, the tab says so; containers on the host network cannot be attributed and are listed as not monitored.

## Image updates

For each container Haetae decides whether a newer image is available and whether it is old enough to trust. A new image waits 48 hours before it counts as eligible, unless it fixes a critical vulnerability of the running image. An administrator can tick **Automatically pull eligible image updates** on the tab. This is off by default. Even when on, Haetae only pulls the image; it never restarts anything. You redeploy when you are ready.

## Asking about security

In chat, `/security` gives a short security digest for all hosts, and `/security hel1-prod` for one host. It lists open critical and high findings, what is new, risky settings, available image fixes and the health of the scans, and it states failed or pending scans as unknown. See [Chat commands](./chat-commands).
