# Understanding alerts and incidents

Haetae watches your hosts all the time and raises an alert when a rule is broken. These rules run on the server every 15 seconds and need no AI, so alerts keep working even when the operator is switched off or its provider is down.

## Alerts, incidents and severity

An **alert** is one rule firing. A container that restarts again and again is one alert; a disk that fills up is another. Alerts on the same host are grouped into one **incident**, so that when hel1-prod has a bad minute you look at one thing and not at twelve messages. The **Incidents** page lists them under **Open** and **Resolved**. Open an incident to see its alerts, each with the evidence behind it.

Every alert has a severity. **High** and **critical** alerts are sent to your chat right away. **Low** and **medium** ones wait for the daily briefing and are visible under Incidents.

Here is what the rules look for. A container that was restarted three or more times in ten minutes (a crash loop) is high. A container killed because it ran out of memory is high. A container whose health check reports unhealthy for a minute is medium. A host that has not been heard from for the **Offline after seconds** time is critical. CPU, memory or disk above the threshold for the **Sustained seconds** time is medium, and disk at 97 percent or more is critical. A deploy that failed, rolled back or failed to roll back is high. A new high or critical vulnerability in a running container raises an alert too; see [Watching vulnerabilities](./watching-vulnerabilities).

For example: `shop-api` leaks memory and the kernel kills it twice in ten minutes. You get a high-severity message from hel1-prod in chat, with buttons and a link to the incident. The incident shows the out-of-memory alerts and, below them, a short explanation written by the operator if it is turned on.

## The messages in chat

An incident message can be answered with buttons: **Later** snoozes it, **Mute 30 days** silences that host's notifications for a month, and **Acknowledge** says you have seen it. Only administrators with a linked chat account can press them. Delivery is retried up to eight times, and a message can arrive more than once but is not lost.

## The daily briefing

Once a day, at the **Briefing hour** in your time zone, Haetae sends a short briefing. On a quiet day it is a single line such as `08:00 briefing, 3 hosts, 24 containers, all running.` It is never longer than three lines and 300 characters, and it always includes open incidents. If the server was down at that hour, it catches up when it is back within six hours, otherwise it skips that day.

If you use the operator, the briefing may be reworded by the AI, but only if it keeps every number, name and state word of the plain text. Otherwise you get the plain text.

## Changing the thresholds

Administrators open **Settings**, then **Alerts**. Here you set the **Time zone** (default UTC), the **Briefing hour (0-23)** (default 8), the thresholds **CPU %**, **Memory %** and **Disk %** (default 90 each), **Sustained seconds** (default 300) and **Offline after seconds** (default 120). Press **Save alert settings**.

## Muting

A mute stops notifications but never hides anything: the alerts are still recorded and shown. Use it when something noisy is expected, such as a planned migration of the database. On an open incident, administrators can type a number of days (up to 90) and press the button **Mute this host for N days**. Active mutes are listed under **Settings**, **Alerts**, **Mutes**, where an administrator can **Remove** one.

If you get no messages when you expected them, see [Chat does not respond](../problems/#chat-does-not-respond).
