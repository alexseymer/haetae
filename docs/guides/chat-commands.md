# Chat commands

In a chat channel that you have linked, you can write to the Haetae bot. Everything that is not one of the commands below is treated as a question for the operator. See [Asking the operator questions](./asking-the-operator).

## Linking

`/link CODE` links your chat account to your Haetae user. You get the code under **Settings**, **Chat channels**, **Link a channel**. In Slack, write `link CODE` without the slash as a direct message. `/start` from an account that is not linked yet answers with a reminder to link. Any other text from an unlinked account is ignored.

## Summaries

`/logs` summarises warnings and errors, and `/security` gives the security digest. Both take an optional host name, and `/logs` also takes a window of `1h`, `6h` or `24h`. Examples:

```
/logs hel1-prod 6h
/security hel1-prod
```

The command word is not case sensitive and may carry the bot's name, as in `/logs@haetae_bot`. If the operator is not enabled, you get the short reply saying so. If it is enabled but the AI cannot be used (for example it is unreachable or over its daily cap), you get a plain rule-based text that says why. An answer arrives as a separate message after a moment, and if the operator is busy with other questions you are asked to try again shortly. More: [Summarising logs](./summarising-logs) and [Watching vulnerabilities](./watching-vulnerabilities).

## Replying to alerts

Messages with buttons are answered by pressing them (**Later**, **Mute 30 days**, **Acknowledge**, **Approve**, **Decline**). On Signal, which has no buttons, reply with `later`, `mute` or `ack` followed by the reference at the end of the message, for example `ack 3f9a1c2b`. Signal accounts cannot approve operator proposals; do that in the web interface or another channel. Email messages carry no buttons.

## Questions

Write anything else as a plain sentence, for example "why was shop-api slow this morning?". If the operator is off, you get a short reply saying so.
