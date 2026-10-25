import type { AlertSettings } from "../api/m6Types";

export interface AlertSettingsErrors {
  timeZone?: string;
  briefingHour?: string;
  cpuPct?: string;
  memPct?: string;
  diskPct?: string;
  sustainedS?: string;
  offlineAfterS?: string;
}

export function isValidTimeZone(tz: string): boolean {
  if (!tz.trim()) return false;
  try {
    Intl.DateTimeFormat(undefined, { timeZone: tz.trim() });
    return true;
  } catch {
    return false;
  }
}

function intInRange(n: number, min: number, max: number): boolean {
  return Number.isInteger(n) && n >= min && n <= max;
}

/** Client-side validation matching org_settings CHECK constraints. */
export function validateAlertSettings(s: AlertSettings): AlertSettingsErrors {
  const errors: AlertSettingsErrors = {};
  if (!isValidTimeZone(s.timeZone)) {
    errors.timeZone = "Enter a valid IANA time zone (e.g. UTC or Europe/Berlin).";
  }
  if (!intInRange(s.briefingHour, 0, 23)) {
    errors.briefingHour = "Briefing hour must be an integer from 0 to 23.";
  }
  if (!intInRange(s.cpuPct, 1, 100)) {
    errors.cpuPct = "CPU threshold must be an integer from 1 to 100.";
  }
  if (!intInRange(s.memPct, 1, 100)) {
    errors.memPct = "Memory threshold must be an integer from 1 to 100.";
  }
  if (!intInRange(s.diskPct, 1, 100)) {
    errors.diskPct = "Disk threshold must be an integer from 1 to 100.";
  }
  if (!intInRange(s.sustainedS, 10, 86400)) {
    errors.sustainedS = "Sustained seconds must be an integer from 10 to 86400.";
  }
  if (!intInRange(s.offlineAfterS, 30, 86400)) {
    errors.offlineAfterS = "Offline after seconds must be an integer from 30 to 86400.";
  }
  return errors;
}

export function hasAlertSettingsErrors(errors: AlertSettingsErrors): boolean {
  return Object.keys(errors).length > 0;
}
