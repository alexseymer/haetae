import { describe, expect, it } from "vitest";
import {
  hasAlertSettingsErrors,
  isValidTimeZone,
  validateAlertSettings,
} from "./alertSettings";
import type { AlertSettings } from "../api/m6Types";

const valid: AlertSettings = {
  timeZone: "UTC",
  briefingHour: 8,
  cpuPct: 90,
  memPct: 90,
  diskPct: 90,
  sustainedS: 300,
  offlineAfterS: 120,
};

describe("isValidTimeZone", () => {
  it("accepts IANA zones via Intl", () => {
    expect(isValidTimeZone("UTC")).toBe(true);
    expect(isValidTimeZone("Europe/Berlin")).toBe(true);
  });

  it("rejects nonsense", () => {
    expect(isValidTimeZone("Not/AZone")).toBe(false);
    expect(isValidTimeZone("")).toBe(false);
  });
});

describe("validateAlertSettings", () => {
  it("accepts defaults", () => {
    expect(hasAlertSettingsErrors(validateAlertSettings(valid))).toBe(false);
  });

  it("rejects out-of-range fields", () => {
    const errors = validateAlertSettings({
      ...valid,
      briefingHour: 24,
      cpuPct: 0,
      offlineAfterS: 10,
      sustainedS: 5,
      timeZone: "Nope",
    });
    expect(errors.briefingHour).toBeTruthy();
    expect(errors.cpuPct).toBeTruthy();
    expect(errors.offlineAfterS).toBeTruthy();
    expect(errors.sustainedS).toBeTruthy();
    expect(errors.timeZone).toBeTruthy();
  });
});
