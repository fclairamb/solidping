/**
 * @vitest-environment jsdom
 *
 * Spec 2026-11-24-01: the tcp form offers a Unix socket target only where it
 * can run — the server allowlist is set (`localSocketOnServer`), or the check
 * is pinned to private locations only — and always shows a stored socket.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import type { CheckTypeInfo } from "@/api/hooks";
import { tcpModule } from "./types/network";
import { CheckFormFieldsProvider } from "./types/context";
import { isLocalSocketOffered, pinnedToPrivateOnly } from "./local-socket";

const Fields = tcpModule.Fields;

const tcpInfo = (localSocketOnServer: boolean): CheckTypeInfo => ({
  type: "tcp",
  description: "",
  labels: [],
  enabled: true,
  supportsLocalSocket: true,
  localSocketOnServer,
});

function renderTcp(opts: {
  config: Record<string, unknown>;
  info: CheckTypeInfo;
  regions: string[];
  placement?: string;
}) {
  render(
    <CheckFormFieldsProvider
      value={{
        type: "tcp",
        org: "acme",
        connections: [],
        configPrivateKeys: undefined,
        name: "",
        setName: () => {},
        tunneled: false,
        checkTypeInfo: opts.info,
        privateRegionsOnly: pinnedToPrivateOnly(
          opts.placement ?? "pinned",
          opts.regions,
        ),
      }}
    >
      <Fields
        state={tcpModule.fromConfig(opts.config)}
        onChange={() => {}}
        errors={[]}
      />
    </CheckFormFieldsProvider>,
  );
}

afterEach(() => {
  cleanup();
});

describe("tcp unix socket target", () => {
  it("is absent with no server allowlist and no private region", () => {
    renderTcp({ config: {}, info: tcpInfo(false), regions: ["eu-west"] });
    expect(screen.queryByTestId("tcp-target-mode")).toBeNull();
    expect(screen.queryByTestId("tcp-socket-path")).toBeNull();
    expect(screen.getByTestId("check-host-input")).toBeTruthy();
  });

  it("is offered once only private regions are selected", () => {
    renderTcp({ config: {}, info: tcpInfo(false), regions: ["@office"] });
    expect(screen.getByTestId("tcp-target-mode")).toBeTruthy();
  });

  it("is offered when the server allowlist is set", () => {
    renderTcp({ config: {}, info: tcpInfo(true), regions: [] });
    expect(screen.getByTestId("tcp-target-mode")).toBeTruthy();
  });

  it("always shows a stored socket path when editing", () => {
    renderTcp({
      config: { socket: "/run/app/app.sock" },
      info: tcpInfo(false),
      regions: ["eu-west"],
    });
    expect(screen.getByTestId("tcp-target-mode")).toBeTruthy();
    expect(
      (screen.getByTestId("tcp-socket-path") as HTMLInputElement).value,
    ).toBe("/run/app/app.sock");
    expect(screen.queryByTestId("check-host-input")).toBeNull();
  });
});

describe("local socket decision", () => {
  it("needs the type capability", () => {
    expect(isLocalSocketOffered(undefined, true)).toBe(false);
    expect(
      isLocalSocketOffered({ supportsLocalSocket: false, localSocketOnServer: true }, true),
    ).toBe(false);
    expect(isLocalSocketOffered({ supportsLocalSocket: true }, true)).toBe(true);
    expect(isLocalSocketOffered({ supportsLocalSocket: true }, false)).toBe(false);
  });

  it("only counts a pinned, all-private placement", () => {
    expect(pinnedToPrivateOnly("pinned", ["@office", "@lab"])).toBe(true);
    expect(pinnedToPrivateOnly("pinned", ["@office", "eu-west"])).toBe(false);
    expect(pinnedToPrivateOnly("pinned", [])).toBe(false);
    expect(pinnedToPrivateOnly("auto", ["@office"])).toBe(false);
  });

  it("serializes the socket mode without host or port", () => {
    const state = {
      ...tcpModule.fromConfig({ host: "acme.com", port: 443 }),
      targetMode: "socket" as const,
      socket: "/run/app/app.sock",
    };
    const { config, errors } = tcpModule.toConfig(state);
    expect(config).toEqual({ socket: "/run/app/app.sock" });
    expect(errors).toEqual([]);
    expect(
      tcpModule.toConfig({ ...state, socket: "" }).errors.map((e) => e.name),
    ).toEqual(["socket"]);
  });
});
