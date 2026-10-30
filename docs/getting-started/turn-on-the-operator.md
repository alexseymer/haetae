# Turn on the AI operator

Everything so far works without any AI. Alerts, incidents, the briefing, deploys and scans are all rule-based. The AI operator is an extra that you can add later. It lets you ask questions in plain language, explains incidents, and, if you allow it, can restart a crashed container. With nothing configured, Haetae never makes a connection to an AI service.

There are two switches. First the server needs to know which AI service to use. Then an administrator turns the operator on in the interface.

## 1. Tell the server which AI to use

Put these settings into `/opt/haetae/haetae.env` and apply them:

`HAETAE_AI_PROVIDER` is `anthropic`, `openai` or `local`. `HAETAE_AI_MODEL` is the model name and is required. `HAETAE_AI_API_KEY` is your key; it is required for the hosted providers and optional for a local one. `HAETAE_AI_BASE_URL` is the address of the service; it has a sensible default for `anthropic` and `openai` and is required for `local`.

For a model running on your own network, for instance through Ollama, it looks like this:

```
HAETAE_AI_PROVIDER=local
HAETAE_AI_BASE_URL=http://10.0.0.5:11434/v1
HAETAE_AI_MODEL=llama3
```

For a service such as OpenRouter you use the `openai` kind with its address:

```
HAETAE_AI_PROVIDER=openai
HAETAE_AI_BASE_URL=https://openrouter.ai/api/v1
HAETAE_AI_MODEL=vendor/model
HAETAE_AI_API_KEY=<your key>
```

Use the model name exactly as the service lists it. The operator works by asking the model to use small tools that look up your data, so the model must support tool calling. Many free models do not, and with such a model the operator can only answer "cause not found". If a setting is wrong, the server refuses to start with a message that begins `HAETAE_AI_*:`, which tells you what to fix.

## 2. Switch it on

As an administrator open **Settings**, then **AI operator**. Tick **Enable the AI operator**, choose the **Autonomy** level and set a **Daily token cap**, then save. The page shows whether the operator is available and how much it used today.

The autonomy level decides what the operator may do. **Report**, the default, only answers and explains. **Suggest** may propose restarting a crashed container, and an administrator approves each proposal. **Act** may restart crashed containers by itself. Start with Report. The levels are explained in [Concepts](../concepts/#autonomy-levels-report-suggest-act).

The daily token cap limits how much AI text Haetae uses per day (counted in UTC). The default is 200,000, and 0 means no AI use at all. At the cap the operator pauses until the next day, and rule-based alerts carry on.

## 3. Test the connection

Press **Test connection** on the same page. It sends two fixed requests that contain none of your data and shows how long the service took and whether the model used a tool. If something is wrong it tells you the reason and a fix. A model that does not use tools shows "tool calling: not used by this model", and you should choose a different model.

## Be honest with yourself about the state of this

The operator has been tested against a scripted stand-in for an AI service, not against a real model, so how good its answers are with a real model has not been measured. See [What is tested](../tested). For what happens to your data on the way, read [Redaction and what leaves your server](../concepts/#redaction-and-what-leaves-your-server).

To use it, read [Asking the operator questions](../guides/asking-the-operator). If it says it is unavailable, see [The operator says unavailable](../problems/#the-operator-says-unavailable).
