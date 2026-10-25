/** Env var name hint for an unconfigured channel (never show values). */
export function channelEnvHint(name: string): string | null {
  switch (name) {
    case "telegram":
      return "HAETAE_TELEGRAM_TOKEN";
    case "slack":
      return "HAETAE_SLACK_APP_TOKEN";
    case "email":
      return "HAETAE_SMTP_ADDR";
    case "signal":
      return "HAETAE_SIGNAL_URL";
    case "web":
      return null;
    default:
      return null;
  }
}
