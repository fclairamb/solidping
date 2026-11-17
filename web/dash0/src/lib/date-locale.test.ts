import { format } from "date-fns";
import { describe, expect, it } from "vitest";
import { de, enUS, es, fr } from "date-fns/locale";
import { dateFnsLocale } from "./date-locale";

describe("dateFnsLocale", () => {
  it("maps supported languages", () => {
    expect(dateFnsLocale("fr")).toBe(fr);
    expect(dateFnsLocale("de")).toBe(de);
    expect(dateFnsLocale("es")).toBe(es);
    expect(dateFnsLocale("en")).toBe(enUS);
    expect(dateFnsLocale("fr-CA")).toBe(fr);
  });

  it("falls back to enUS", () => {
    expect(dateFnsLocale("xx")).toBe(enUS);
    expect(dateFnsLocale(undefined)).toBe(enUS);
  });

  it("prints localized month names", () => {
    const d = new Date(2026, 10, 17, 20, 48, 32);
    expect(format(d, "MMM d, HH:mm:ss", { locale: dateFnsLocale("fr") })).toContain("nov.");
    expect(format(d, "MMM d, HH:mm:ss", { locale: dateFnsLocale("en") })).toContain("Nov");
  });
});
