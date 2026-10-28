# hello: a minimal app to deploy with Haetae

One service, `web`, running the official `nginx:1.27-alpine` image with its default welcome page. There is nothing to build, so a deploy is a pull and a start. It publishes host port 8088 and has a healthcheck, so Haetae's "Checking" step has something to verify.

It is checked against the validation rules in `internal/deployspec` (project name, compose size and path). It has not been run through Docker by the author of this example; the first deploy on your host is the real test.

## Deploy it from a pasted compose file

1. In the Haetae UI open the host, **Deploy** tab, **New project**.
2. **Name:** `hello`. **Source:** **Pasted compose file**. Paste the contents of [compose.yaml](compose.yaml).
3. Leave **Domain**, **Service** and **Port** empty (see below for HTTPS).
4. **Create project**, then **Deploy now** on the project page.
5. When the status reaches **Healthy**, the result reads `healthy; published ports: web:8088->80/tcp`. Open `http://<host address>:8088/`.

## Deploy it from Git

Choose **Git repository** instead: **Repository URL** `https://github.com/alexseymer/haetae`, **Ref** `main`, **Compose path** `examples/hello/compose.yaml`. This works once the example is on `main`.

## Add HTTPS

Fill all three ingress fields together: **Domain** (a name whose DNS points at the host; ports 80 and 443 must be free there), **Service** `web`, **Port** `80`. The result becomes `https://<domain>`. Public certificate issuance has not been tested in this project; see [docs/deploy.md](../../docs/deploy.md) and [docs/troubleshooting.md](../../docs/troubleshooting.md).

## Roll back and remove

Deploy history on the project page has **Rollback**. **Remove** stops the container and keeps volumes (this example has none). Full details: [docs/deploy.md](../../docs/deploy.md).
