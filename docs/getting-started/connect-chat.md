# Connect a chat channel

Haetae tells you about problems in chat, so you do not have to watch a dashboard. You get a message when something serious happens on hel1-prod, and one short briefing each morning. The web chat is always there. Telegram, Slack, email and Signal are optional and each is switched off until you configure it. A channel that is off makes no network connection at all.

Open **Settings**, then **Chat channels**. Under **Channels** you see which channels are enabled and which are not configured, with the setting each one needs. Settings go into `/opt/haetae/haetae.env` on the Haetae server (see [Install the server](./install-the-server)). Apply them by running the installer again or with `docker compose up -d` in `/opt/haetae`.

To receive messages, you link your own account to a channel with a short one-time code. Open **Settings**, **Chat channels**, **Link a channel**, and press the button for your channel. The code works once and expires after 10 minutes.

## Telegram

Create a bot with Telegram's BotFather and put its token into `HAETAE_TELEGRAM_TOKEN`. Apply the setting. Press **Link Telegram** to get a code, then send `/link CODE` to your bot in a private chat. The bot replies "Linked. You will receive alerts here."

## Slack

Create a Slack app with Socket Mode switched on, an app-level token and a bot token, interactivity on (so the buttons work), and direct-message events. Put the tokens into `HAETAE_SLACK_APP_TOKEN` and `HAETAE_SLACK_BOT_TOKEN`; both are required. Press **Link Slack** and send `link CODE` as a direct message to the bot. Write it without the slash, because Slack keeps messages that start with a slash for its own commands. The exact permissions the Slack app needs have not been checked against Slack.

## Email

Set `HAETAE_SMTP_ADDR` (host and port, the default port is 587) and `HAETAE_SMTP_FROM`. If your mail server needs a login, also set `HAETAE_SMTP_USER` and `HAETAE_SMTP_PASS`. Press **Link email** and then **Send code**. Haetae emails a code to the address you signed in with, never to an address you type, and you enter it in the box **Code from the email**. Emails are send-only and carry no buttons.

## Signal

Signal needs a `signal-cli` service that you set up and run yourself; Haetae does not include one. Point `HAETAE_SIGNAL_URL` at its JSON-RPC HTTP address, press **Link Signal** and send `/link CODE` to the Haetae contact. Signal has no buttons, so a message ends with a line like `Ref: 3f9a1c2b (reply: later|mute|ack <ref>)` and you reply with one of those words and the reference.

## Who can press buttons

Messages about incidents carry buttons: **Later**, **Mute 30 days** and **Acknowledge**, and for operator proposals **Approve** and **Decline**. Only a chat account that is linked to an administrator can use them. Anyone else gets "Action not allowed." Each button works once; pressing it again says "Already handled." You can remove a linked account under **Linked accounts** with **Unlink**.

Note that live Telegram, Slack, Signal and email services have not been tried yet; they were tested against stand-ins. Expect to fix small details on first use. See [What is tested](../tested).

How alerts, incidents and the briefing behave is described in [Understanding alerts and incidents](../guides/alerts-and-incidents).

Next: [Turn on the AI operator](./turn-on-the-operator).
