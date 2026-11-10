/**
 * @vitest-environment jsdom
 *
 * The secret encryption choice of the check form (spec 2026-11-08-01).
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { SecretEncryptionPanel } from "./secret-encryption";

afterEach(cleanup);

const base = {
  onModeChange: () => undefined,
  required: false,
  blocker: null,
  recipients: [],
  regions: ["@dc1"],
  storedSealed: false,
  switchedNotice: false,
} as const;

describe("SecretEncryptionPanel", () => {
  it("shows the lock badge with the agent count in browser mode", () => {
    render(
      <SecretEncryptionPanel
        {...base}
        mode="browser"
        recipients={[
          { kind: "agent", region: "@dc1", agentName: "a", recipient: "age1a", fingerprint: "aa" },
          { kind: "agent", region: "@dc1", agentName: "b", recipient: "age1b", fingerprint: "bb" },
        ]}
      />,
    );
    expect(screen.getByTestId("sealed-secret-badge").textContent).toContain("@dc1 (2 agents)");
    expect(screen.getAllByTestId("sealed-secret-recipient")).toHaveLength(2);
  });

  it("says region key when the region uses a shared key", () => {
    render(
      <SecretEncryptionPanel
        {...base}
        mode="browser"
        recipients={[{ kind: "region", region: "@dc1", recipient: "age1r", fingerprint: "rr" }]}
      />,
    );
    expect(screen.getByTestId("sealed-secret-badge").textContent).toContain("region key");
  });

  it("blocks saving without recipients", () => {
    render(<SecretEncryptionPanel {...base} mode="browser" />);
    expect(screen.getByTestId("sealed-secret-no-recipient").textContent).toContain("@dc1");
  });

  it("disables the browser choice with its reason on a shared region", () => {
    render(<SecretEncryptionPanel {...base} mode="server" blocker="sharedRegion" />);
    expect((screen.getByTestId("secret-encryption-mode-browser") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId("secret-encryption-blocker")).toBeTruthy();
    // Positive control: the server choice is there.
    expect(screen.getByTestId("secret-encryption-mode-server")).toBeTruthy();
  });

  it("drops the server choice in a required org", () => {
    render(<SecretEncryptionPanel {...base} mode="browser" required />);
    expect(screen.queryByTestId("secret-encryption-mode-server")).toBeNull();
    expect(screen.getByTestId("secret-encryption-mode-browser")).toBeTruthy();
  });
});
