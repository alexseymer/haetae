# Asking the operator questions

Once the [AI operator is turned on](../getting-started/turn-on-the-operator), you can ask it questions in plain language, either in the web interface or in a linked chat channel.

## How to ask

In the web interface press **Ask the operator** at the top, or open **Chat**. Type a question into the box and press **Send**. Good questions are about your own servers and are specific:

- Why was shop-api slow this morning?
- What changed on hel1-prod before the restart?
- Which container uses the most memory?

In Telegram, Slack and Signal, just write the question to the bot after you have linked your account. In email you cannot ask questions.

## How it answers

The operator does not guess from general knowledge. It looks things up in your own data with a handful of read-only tools: hosts, containers, metrics, events such as alerts and incidents, deploys, security findings, and, when needed, recent log lines. Log lines are cleaned on the host first, and at most 200 lines from a window of at most 24 hours are fetched. It can make at most eight lookups per question, and each question stands alone, with no memory of the one before.

Every statement in the answer must point to evidence, shown as links to the host, container, deploy, incident or log view it came from. A statement without valid evidence is dropped. If nothing is left, the reply is exactly **cause not found**. That is a deliberate, honest answer: it means the data does not show a cause, and not that nothing is wrong. Open the links and check them; they are the point.

## When it does not answer

Sometimes you get "The operator is unavailable" followed by a reason in brackets. The reasons are explained in [Problems](../problems/#the-operator-says-unavailable). Questions about containers can also be refused by the AI provider's own safety filter, in which case you see that the provider's safety filter blocked the question and are advised to rephrase it or check the provider settings. Such a question is not guaranteed to work with every provider and model, so treat answers about containers as best effort and try rewording if needed.

With the operator off, a question gets the fixed reply that the operator is not enabled yet and that rule alerts are active.

## What it never does

The operator cannot delete anything, run a shell, deploy or change a setting. The only action it can ever take is restarting a crashed container, and only at the autonomy level you allow. See [Concepts](../concepts/#autonomy-levels-report-suggest-act).

There are also two shortcuts for summaries: see [Summarising logs](./summarising-logs) and [Chat commands](./chat-commands).
