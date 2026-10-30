# Deploy from Git

When your shop lives in a Git repository, Haetae can fetch it, build it on the host and keep it up to date. This guide continues from [Deploy an app](../getting-started/deploy-an-app).

## What happens during a deploy

When you press **Deploy now**, the agent on hel1-prod takes a shallow copy of the branch you named. It builds the images with `docker compose build --pull`, which is allowed up to 15 minutes. Then it starts the services with `docker compose up -d --remove-orphans --wait`, allowed up to 3 minutes. Containers of services that you deleted from the compose file are removed; volumes are never touched. Finally Haetae checks that every service is running, and where a service has a healthcheck, that it stays healthy for ten seconds. If all of that works the deploy is **Healthy**.

The project page shows the steps as a progress bar and streams the build log. Only one deploy per project can run at a time. A short gap appears while old containers are replaced; this is not a zero-downtime deploy, because swapping a database container underneath shared volumes is not safe.

The log you see has been filtered on the host first, so that secrets which appear in the build output do not reach the Haetae server. This is pattern based, so do not make your build print secrets on purpose.

## Redeploy on every push

Open the project, and for a Git project tick **Auto-deploy on push** (the same setting exists as **Auto-deploy** on the project page). From then on the agent asks the Git server every ten seconds whether the branch has a new commit. When it does, a deploy starts on its own and is marked as triggered by a push. There is no webhook and no open port. The delay between your `git push` and the start is therefore at most ten seconds, plus the time the build takes.

Use a branch name as the ref. A fixed commit id never changes, so auto-deploy would never fire.

Imagine you fix a bug in `shop-api` and push to `main`. Within about ten seconds hel1-prod starts a deploy, you watch it go through building and starting, and a minute or two later it is healthy. If you introduced a build error, the deploy ends as **Failed** and the old version keeps serving customers.

## Limits

Git over `https://` only: no SSH addresses, and no user name or password in the URL. Use the **Access token** field for private repositories. There are no buildpacks and no preview deployments. If the host is offline when you remove a project, tick **Force delete records (host is offline)**.

See also [Rolling back](./rolling-back) and, when something fails, [A deploy fails](../problems/#a-deploy-fails).
