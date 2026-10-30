# Environment variables

Settings for the Haetae server go into `/opt/haetae/haetae.env`, one `KEY=value` per line. Apply them by running the installer again or with `docker compose up -d` in `/opt/haetae`. Settings for the installer are given in front of the command, as in `sudo HAETAE_HOST=haetae.example.com sh install-server.sh`.

## When you install

`HAETAE_IMAGE` is the server image to use; until a published image exists, set it to the one you built, such as `haetae-server:local`. `HAETAE_NO_PULL=1` tells the installer not to download the image. `HAETAE_HOST` is the name or address your hosts use to reach the server, and it goes into the server's certificate; by default the installer takes the machine's first address. `HAETAE_WEB_PORT` and `HAETAE_GATEWAY_PORT` change the ports 8080 and 8443. `HAETAE_DIR` changes the install folder from `/opt/haetae`. `COMPOSE_PROJECT_NAME` changes the Compose project name from `haetae`.

## Web address and certificate

`HAETAE_WEB_URL` is the address people type to open the interface, and it is shown in enrollment commands. `HAETAE_PUBLIC_URL` is the address used for links in chat and email messages; it defaults to `HAETAE_WEB_URL`. `HAETAE_WEB_TLS_CERT` and `HAETAE_WEB_TLS_KEY` are the paths of your own certificate and key; set both or neither. `HAETAE_TRUSTED_PROXIES` lists the addresses of reverse proxies whose forwarded client address may be believed. `HAETAE_GATEWAY_NAMES` is a comma-separated list of names or addresses for the server's certificates and defaults to `HAETAE_HOST`.

## Chat

`HAETAE_TELEGRAM_TOKEN` is the token of your Telegram bot. `HAETAE_SLACK_APP_TOKEN` and `HAETAE_SLACK_BOT_TOKEN` are the two Slack tokens, and both are required. `HAETAE_SMTP_ADDR` is the mail server as `host:port`, `HAETAE_SMTP_FROM` the sender address, and `HAETAE_SMTP_USER` and `HAETAE_SMTP_PASS` the optional login. `HAETAE_SIGNAL_URL` is the address of your own `signal-cli` service. `HAETAE_CHAT_SECRET` is an optional secret for signing buttons, at least 32 bytes; if you leave it out, one is created and stored.

## AI operator

`HAETAE_AI_PROVIDER` is `anthropic`, `openai` or `local`; empty or `off` switches the AI off. `HAETAE_AI_MODEL` is the model name. `HAETAE_AI_API_KEY` is the key for the provider. `HAETAE_AI_BASE_URL` is the provider's address; it is optional for `anthropic` and `openai`, required for `local`, and must use `https` except for a local model.

## Vulnerability database

`HAETAE_VULN_DB_URL` is where the server looks for the vulnerability database; the installed stack points it at the public list, and `off` stops all downloads. `HAETAE_VULN_DB_DIR` is the folder where it is stored.

## On a host (the agent)

`HAETAE_INVENTORY_MEM` is how much memory the scanning helper may use, 256 MiB by default, and accepts suffixes like `640M`. `HAETAE_INVENTORY_TIMEOUT` is how long one scan may run, ten minutes by default. Set both in a systemd override for `haetae-agent`. `HAETAE_SKIP_SYSTEMD=1` makes the host installer install and enroll but not create the service.
