# Enroll a host

A host is a Linux server with Docker that you want Haetae to watch and deploy to. In our example it is hel1-prod, the server that runs the shop. Enrolling means installing a small program, the agent, on that server and telling it where your Haetae server is.

The agent connects outward to your Haetae server. It opens no port of its own, so you do not need to change the firewall of hel1-prod. The only exception is that your deployed apps need ports 80 and 443 if you want HTTPS for them.

## Enroll with one command

1. In the web interface open **Hosts** and press **Add host**.
2. Enter a **Host name**, for example `hel1-prod`, and a **Validity (hours)** for the token, between 1 and 168 (the default is 24). Press **Create token**.
3. Copy the command that appears and run it on hel1-prod as root. The token inside works only once.

The command downloads the installer from your own Haetae server, so hel1-prod does not need access to GitHub. Its shape is:

```sh
curl -fsS -k --pinnedpubkey '<web key pin>' 'https://haetae.example.com:8080/install.sh' | sh -s -- \
  --server 'https://haetae.example.com:8443' --token '<token>' --ca-pin 'sha256:<fingerprint>' \
  --web 'https://haetae.example.com:8080' --web-pin '<web key pin>'
```

The `-k` only skips the check against public certificate authorities. The `--pinnedpubkey` part is what makes sure you are talking to your own Haetae server and not somebody else.

After a few seconds hel1-prod shows as **Online** on the Hosts page. Charts for CPU, memory, disk and network fill in within minutes.

## What the installer does on the host

It checks that you are root and that `curl` and `sha256sum` exist, downloads the agent and a helper program used for scanning, and verifies both against a checksum list before it installs anything. It puts them in `/usr/local/bin`, creates a folder `/var/lib/haetae-agent` for the agent's identity and state, enrolls with your token, creates a system user called `haetae-agent`, and starts the agent as a systemd service that restarts automatically.

The agent needs access to Docker to see your containers, so the installer adds its user to the `docker` group and prints a warning. Treat this seriously: access to Docker is practically root access on that machine. If you re-run the command later, an already enrolled host is not enrolled again; only the programs are updated.

To read the agent's log on the host, use `journalctl -u haetae-agent`.

## Limiting what the operator may do on one host

The AI operator's autonomy level (see [Concepts](../concepts/#autonomy-levels-report-suggest-act)) is set for the whole organisation, but each host can lower it. The agent takes a setting `--max-autonomy` with the values `report`, `suggest` or `act`, and the lower of the two settings wins. For example, to make sure the operator can never restart anything on hel1-prod, create a systemd override:

```sh
sudo systemctl edit haetae-agent
# in the editor:
# [Service]
# ExecStart=
# ExecStart=/usr/local/bin/haetae-agent run --state-dir /var/lib/haetae-agent --max-autonomy report
sudo systemctl restart haetae-agent
```

## Removing a host

As an administrator, open the host and press **Remove host**. The agent is disconnected at once and cannot come back with its old certificate. The host disappears from lists, alerts and briefings. Nothing is deleted from the database and the containers on the server keep running. The agent software stays installed until you remove it from the server yourself: stop and disable the service, delete `/etc/systemd/system/haetae-agent.service`, `/usr/local/bin/haetae-agent`, `/usr/local/bin/haetae-inventory` and `/var/lib/haetae-agent`, and remove the `haetae-agent` user. The `haetae-caddy` container and the containers of deployed projects are left alone. You can enroll the same host name again later with a new token.

To only pause a host without removing it, run `sudo systemctl disable --now haetae-agent` on it. It then shows as offline and keeps its history.

One thing to plan for: an agent's certificate is valid for one year, and there is no automatic renewal yet. Before a year has passed, remove the host and enroll it again.

If the host does not come online, see [A host shows offline](../problems/#a-host-shows-offline).

Next: [Deploy an app](./deploy-an-app).
