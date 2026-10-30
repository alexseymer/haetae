# Getting started

This part takes you from nothing to a working setup in six short steps. You do not need to do all of them on day one. Steps 1 to 4 give you a server you can watch and deploy to; steps 5 and 6 add chat messages and the AI operator, and you can leave them for later.

Throughout the documentation we follow one small shop. It has three parts: an **api**, a **web** frontend and a **database**, all running as Docker Compose containers on one Linux server called **hel1-prod**. In the Haetae screenshots the containers are named `shop-api`, `shop-web` and `shop-db`. If your own setup looks similar, you can read the examples as your own.

1. [Install the server](./install-the-server). Haetae itself runs as two containers on a machine you control. This is the "control plane": the web interface and the brain.
2. [First login](./first-login). Create the first administrator account.
3. [Enroll a host](./enroll-a-host). Connect hel1-prod, the machine that runs your shop, with one command.
4. [Deploy an app](./deploy-an-app). Put the shop (or the small example app) on the host.
5. [Connect a chat channel](./connect-chat). Get alerts and the daily briefing in Telegram, Slack, email or Signal.
6. [Turn on the AI operator](./turn-on-the-operator). Optional. Ask questions in plain language.

Before you start, check what is not available yet. There is no published container image, so you build the server image yourself from the source code, which takes a few minutes. The page on [what is tested and what is not](../tested) explains which parts have been tried against real services and which have not.
