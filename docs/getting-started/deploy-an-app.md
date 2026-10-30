# Deploy an app

Haetae deploys Docker Compose apps to a host you have enrolled. The app can come from a Git repository or from a compose file that you paste into the interface. The build and `docker compose up` run on the host itself; your source code never passes through the Haetae server.

Only administrators can deploy. A deploy runs with whatever rights the compose file asks for, and a compose file can mount the whole disk or run privileged, so deploying is root access on that host. The interface tells you so.

## Try it with the example app

The repository contains a tiny example that is the quickest way to see everything work. It is one nginx web page, and it needs no build.

1. Open **Hosts**, open your host (hel1-prod), go to the **Deploy** tab and press **New project**.
2. Enter the name `hello`. As the source choose **Pasted compose file**, and paste the contents of `examples/hello/compose.yaml` from the repository.
3. Leave **Domain**, **Service** and **Port** empty for now and press **Create project**.
4. On the project page press **Deploy now**.

You will see the deploy move through its steps: queued, fetching, building, starting, checking, and then healthy. The build output streams on the page while it happens. When the status reads **Healthy**, the result lists the published port. Open `http://<address of hel1-prod>:8088/` and you see the nginx welcome page.

## Deploy the shop

Now the real thing. Suppose the shop lives in a Git repository, `https://github.com/example/shop.git`, with a `compose.yaml` in its main folder that defines the services `api`, `web` and `db`.

1. On the **Deploy** tab press **New project**, name it `shop`, and choose **Git repository** as the source.
2. Enter the **Repository URL** (plain `https://` only, with no user name or password in it), and the **Ref**, which is a branch name such as `main`. Leave **Compose path** empty if the file is called `compose.yaml` in the root, otherwise enter its path.
3. If the repository is private, paste an **Access token**. It is stored encrypted on the Haetae server and is never shown again.
4. Press **Create project**, then **Deploy now**.

Project names use lowercase letters, digits and dashes, at most 40 characters, and must be unique on the host.

If the deploy fails, nothing breaks: a failure while building never touches the version that is running, and a version that builds but does not start is rolled back automatically. [Rolling back](../guides/rolling-back) explains this.

## Secrets

Haetae stores one secret for you, the Git access token. It has no place for application secrets such as the database password of `shop-db`. Do not paste them into a pasted compose file expecting protection, because that file is stored as plain text on the server. Keep such values in files or in the environment on the host, or in a private repository that your compose file reads.

For automatic deploys when you push to Git, see [Deploy from Git](../guides/deploy-from-git). For a real address with HTTPS, see [A domain with HTTPS](../guides/domain-and-https).

Next: [Connect a chat channel](./connect-chat).
