import posthog from "posthog-js";

const projectToken = import.meta.env.VITE_POSTHOG_KEY;
const host = import.meta.env.VITE_POSTHOG_HOST;
export const isPostHogEnabled = Boolean(projectToken && host);

if (!isPostHogEnabled) {
  if (import.meta.env.DEV) {
    const missingVariable = projectToken ? "VITE_POSTHOG_HOST" : "VITE_POSTHOG_KEY";
    throw new Error(
      `${missingVariable} variable required by PostHog is missing or un-configured, this causes events to be silently missed. This error stops appearing once ${missingVariable} is configured`,
    );
  }
} else {
  posthog.init(projectToken, {
    api_host: host,
    capture_exceptions: {
      capture_unhandled_errors: true,
      capture_unhandled_rejections: true,
      capture_console_errors: false,
    },
    logs: {
      serviceName: "haetae-web",
      environment: import.meta.env.MODE,
    },
  });
  posthog.logger.info("browser analytics initialized", {
    runtime: "browser",
  });
}

export const posthogLogger = posthog.logger;
export default posthog;
