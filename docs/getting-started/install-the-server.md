# Install the server

The Haetae server is the part you open in your browser. It is two containers, the Haetae server and its PostgreSQL database, started with Docker Compose. In our example you would put it on a small machine of its own, or on hel1-prod itself for a first trial. One machine can be both the server and a managed host.

The self-hosted version has every feature. It does not send data to the Haetae authors. The server only reaches out to three things: the public vulnerability database list (so it can tell you about known security problems), and the chat services and AI provider that you choose to configure.

## What you need

You need a Linux machine with Docker and Compose version 2 (check with `docker compose version`), plus `git` and `curl`. Two TCP ports must be free: 8080 for the web interface and 8443 for the connection that your servers use to reach Haetae. Any server you later want to manage must be able to reach both ports. Haetae never connects to your servers by itself; they connect to it. You also need root access or `sudo`, because the installer writes its files to `/opt/haetae`.

## Build the image and run the installer

There is no published image yet, so you build it from the repository. Clone it anywhere except `/opt/haetae`:

```sh
git clone https://github.com/alexseymer/haetae.git haetae-src
cd haetae-src
docker build -t haetae-server:local .
sudo HAETAE_IMAGE=haetae-server:local HAETAE_NO_PULL=1 sh deploy/install-server.sh
```

The first build includes the web interface and takes several minutes. The two settings in front of the installer tell it to use the image you just built and not to try to download one.

When it finishes, the installer prints a web address such as `https://203.0.113.10:8080` and a one-time setup code. Keep both for the next step.

If the servers you want to manage cannot reach that address (for example because the installer picked a private address), set `HAETAE_HOST` to a name or address they can reach, such as `HAETAE_HOST=haetae.example.com`. This name goes into the server's certificate and into the commands that enroll hosts, so decide it before you enroll anything.

If a port is already in use, the installer stops with a message saying so. You can pick other ports with `HAETAE_WEB_PORT` and `HAETAE_GATEWAY_PORT`.

## Where things live

Everything is under `/opt/haetae`. Your own settings go in `/opt/haetae/haetae.env`, a commented template that the installer creates once and never overwrites. Settings for chat and the AI operator go there, one `KEY=value` per line. After editing it, apply the change by running the installer again, or with `cd /opt/haetae && docker compose up -d`. The other files in the folder are rewritten on every installer run, so do not edit them. The full list of settings is on the [environment variables](../reference/environment-variables) page.

## Your own certificate and a reverse proxy

The first time you open the web address, your browser will warn about the certificate. That is expected: the server issues its own certificate from its own certificate authority. If you have a real certificate, put the files into the server container and set `HAETAE_WEB_TLS_CERT` and `HAETAE_WEB_TLS_KEY` in `haetae.env`. Only the path to a supplied certificate file was tested, not automatic public certificates for the Haetae web address itself.

If you put a reverse proxy in front, it must talk to `https://<server>:8080` (the web port always speaks TLS). Set `HAETAE_WEB_URL` to the address people type and `HAETAE_TRUSTED_PROXIES` to the proxy's address, so that login limits see the real visitor. Pass port 8443 through as plain TCP, not through a proxy that ends TLS, because your servers authenticate with client certificates.

## Backups, upgrades and removal

Two things need a backup: the database and the data volume. The data volume holds the server's own certificate authority and the key that protects stored Git tokens. If you lose them, every host has to be enrolled again and Git tokens must be re-entered. A database dump looks like this:

```sh
docker compose --project-directory /opt/haetae exec -T db pg_dump -U haetae haetae > haetae.sql
```

To upgrade, build or pull the newer image and run the installer again with the same settings. It keeps your database password and data and recreates the containers. To upgrade the small program on a host, run the host's install command again; see [Enroll a host](./enroll-a-host).

To remove Haetae, run `docker compose --project-directory /opt/haetae down -v` and delete `/opt/haetae`. The `-v` deletes the database and data. Leave it out to keep them.

Continue with [First login](./first-login).
