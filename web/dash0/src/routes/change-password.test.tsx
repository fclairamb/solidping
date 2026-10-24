/**
 * @vitest-environment jsdom
 *
 * The forced first-login screen asks for a real email only while the seeded
 * admin still has the default address (spec 2026-10-13-02).
 */
import type { ComponentType, PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import "@/i18n";

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: Record<string, unknown>) => ({ options }),
  Navigate: () => null,
}));

// The split layout (theme toggle, artwork) is not under test.
vi.mock("@/components/layout/auth-split-layout", () => ({
  AuthSplitLayout: ({ children }: PropsWithChildren) => <div>{children}</div>,
}));

const mockUser = vi.hoisted(() => ({
  current: { email: "admin@solidping.io", mustChangeEmail: true } as {
    email: string;
    mustChangeEmail?: boolean;
  },
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: mockUser.current, logout: vi.fn() }),
}));

const changePassword = vi.hoisted(() => vi.fn());
vi.mock("@/api/password", () => ({ changePassword }));

vi.mock("@/api/client", () => ({
  ApiError: class ApiError extends Error {
    code?: string;
  },
  getToken: () => "token",
}));

import { Route } from "./change-password";

const Page = (Route as unknown as { options: { component: ComponentType } }).options.component;

function fill(testId: string, value: string) {
  fireEvent.change(screen.getByTestId(testId), { target: { value } });
}

function fillPasswords() {
  fill("forced-password-current", "solidpass");
  fill("forced-password-new", "brandnewpassword");
  fill("forced-password-confirm", "brandnewpassword");
}

beforeEach(() => {
  changePassword.mockReset();
  changePassword.mockResolvedValue({ message: "ok" });
  mockUser.current = { email: "admin@solidping.io", mustChangeEmail: true };
});

afterEach(() => cleanup());

describe("forced password change screen", () => {
  it("shows the email fields only with mustChangeEmail", () => {
    render(<Page />);
    expect(screen.getByTestId("forced-email")).toBeTruthy();
    expect(screen.getByTestId("forced-email-confirm")).toBeTruthy();
    expect((screen.getByTestId("forced-email") as HTMLInputElement).value).toBe("");

    cleanup();
    mockUser.current = { email: "ops@acme.com" };
    render(<Page />);
    expect(screen.queryByTestId("forced-email")).toBeNull();
    expect(screen.queryByTestId("forced-email-confirm")).toBeNull();
    // Positive control: the password form is still there.
    expect(screen.getByTestId("forced-password-new")).toBeTruthy();
  });

  it("blocks a mismatched email", () => {
    render(<Page />);
    fill("forced-email", "ops@acme.com");
    fill("forced-email-confirm", "other@acme.com");
    fillPasswords();
    fireEvent.click(screen.getByTestId("forced-password-submit"));

    expect(screen.getByTestId("forced-password-error").textContent).toContain("don't match");
    expect(changePassword).not.toHaveBeenCalled();
  });

  it("blocks the default address", () => {
    render(<Page />);
    fill("forced-email", "Admin@SolidPing.io");
    fill("forced-email-confirm", "admin@solidping.io");
    fillPasswords();
    fireEvent.click(screen.getByTestId("forced-password-submit"));

    expect(screen.getByTestId("forced-password-error").textContent).toContain("default");
    expect(changePassword).not.toHaveBeenCalled();
  });

  it("submits no email when the fields are empty", async () => {
    render(<Page />);
    fillPasswords();
    fireEvent.click(screen.getByTestId("forced-password-submit"));

    await waitFor(() => expect(changePassword).toHaveBeenCalledTimes(1));
    expect(changePassword).toHaveBeenCalledWith("brandnewpassword", "solidpass", undefined);
  });

  it("submits the email with the password", async () => {
    render(<Page />);
    fill("forced-email", "ops@acme.com");
    fill("forced-email-confirm", "ops@acme.com");
    fillPasswords();
    fireEvent.click(screen.getByTestId("forced-password-submit"));

    await waitFor(() => expect(changePassword).toHaveBeenCalledTimes(1));
    expect(changePassword).toHaveBeenCalledWith("brandnewpassword", "solidpass", "ops@acme.com");
  });

  it("shows a server refusal inline", async () => {
    changePassword.mockRejectedValue(new Error("This email address is already used by another account"));
    render(<Page />);
    fill("forced-email", "taken@acme.com");
    fill("forced-email-confirm", "taken@acme.com");
    fillPasswords();
    fireEvent.click(screen.getByTestId("forced-password-submit"));

    await waitFor(() =>
      expect(screen.getByTestId("forced-password-error").textContent).toContain("already used")
    );
  });
});
