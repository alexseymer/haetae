# First login

Open the address the installer printed, for example `https://203.0.113.10:8080`. Your browser warns that the certificate is not trusted. The server created that certificate itself, so the warning is expected; continue to the page.

You are asked for the setup code that the installer printed. Enter it, and then choose the name of your organisation (in our example, the shop), and the email address and password of the first administrator. After this the setup code is gone and you sign in with your email and password.

If you lost the setup code and have not finished setup yet, print it again on the server:

```sh
docker compose --project-directory /opt/haetae exec -T server /usr/local/bin/haetae-server setup-code
```

Once setup is done, this command answers that no setup code exists.

## Adding more people

Open **Settings**, then the **Account** tab, then **Add user**. Enter an email address, a password and a role. A **member** can look at everything: hosts, incidents, chat, security results. An **admin** can in addition enroll hosts, deploy, change settings and press buttons in chat messages. Give the admin role only to people who should be allowed to run code on your servers, because deploying is exactly that.

## A tour of the interface

The left side has **Hosts**, **Incidents**, **Chat**, **Help** and **Settings**. The button **Ask the operator** at the top takes you to the chat with the question box ready. The **Help** page explains the terms you will see, such as severity, incident and autonomy, and links to this documentation.

Next: [Enroll a host](./enroll-a-host).
