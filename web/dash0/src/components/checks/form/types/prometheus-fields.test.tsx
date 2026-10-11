/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-15-13: the prometheus form swaps its selector fields with the
 * mode. Scrape mode asks for a metric name (and labels); promql mode replaces
 * them with the query field, so a leftover metric cannot be edited, or
 * submitted, from a mode that ignores it.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { prometheusModule, type PrometheusState } from "./infra";

const Fields = prometheusModule.Fields;

function renderFields(mode: PrometheusState["mode"]) {
  const state: PrometheusState = {
    ...prometheusModule.fromConfig({}),
    mode,
    metric: "process_open_fds",
    query: "sum(up)",
  };
  render(<Fields state={state} onChange={() => {}} errors={[]} />);
}

afterEach(() => {
  cleanup();
});

describe("prometheus fields", () => {
  it("scrape mode shows the metric selector and no query field", () => {
    renderFields("scrape");
    expect(screen.getByTestId("check-prometheus-metric-input")).toBeTruthy();
    expect(screen.getByTestId("check-prometheus-label-add")).toBeTruthy();
    expect(screen.queryByTestId("check-prometheus-query-input")).toBeNull();
  });

  it("promql mode replaces the metric selector with the query field", () => {
    renderFields("promql");
    expect(
      (
        screen.getByTestId(
          "check-prometheus-query-input",
        ) as HTMLTextAreaElement
      ).value,
    ).toBe("sum(up)");
    expect(screen.queryByTestId("check-prometheus-metric-input")).toBeNull();
    expect(screen.queryByTestId("check-prometheus-label-add")).toBeNull();
  });
});
