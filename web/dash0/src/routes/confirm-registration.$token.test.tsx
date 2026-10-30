/**
 * @vitest-environment jsdom
 *
 * The one-shot confirmation token is spent on a click, never on load: mail
 * link scanners open (and sometimes render) the link before the human does.
 */
import type { ComponentType, PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import "@/i18n";

vi.mock("@tanstack/react-router", () => ({
  createFileRoute: () => (options: Record<string, unknown>) => ({
    options,
    useParams: () => ({ token: "tok-123" }),
  }),
  useNavigate: () => vi.fn(),
  Link: ({ children }: PropsWithChildren) => <a>{children}</a>,
}));

vi.mock("@/components/layout/auth-split-layout", () => ({
  AuthSplitLayout: ({ children }: PropsWithChildren) => <div>{children}</div>,
}));

const confirm = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  isPending: false,
}));

vi.mock("@/api/hooks", () => ({
  useConfirmRegistration: () => confirm,
}));

vi.mock("@/lib/confirm-registration-handoff", () => ({
  applyConfirmRegistrationHandoff: () => ({ to: "/no-org" }),
  ConfirmRegistrationSessionError: class ConfirmRegistrationSessionError extends Error {},
}));

import { Route } from "./confirm-registration.$token";

const Page = (Route as unknown as { options: { component: ComponentType } }).options.component;

beforeEach(() => {
  confirm.mutateAsync.mockReset();
  confirm.mutateAsync.mockResolvedValue({ accessToken: "a" });
});

afterEach(() => cleanup());

describe("confirm registration page", () => {
  it("does not spend the token on load", async () => {
    render(<Page />);

    expect(screen.getByTestId("confirm-registration-button")).toBeTruthy();
    // Give any effect a chance to run before asserting the absence.
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(confirm.mutateAsync).not.toHaveBeenCalled();

    // Positive control: the click does spend it.
    fireEvent.click(screen.getByTestId("confirm-registration-button"));
    await waitFor(() => expect(confirm.mutateAsync).toHaveBeenCalledWith({ token: "tok-123" }));
  });

  it("spends the token once on a double click", async () => {
    render(<Page />);

    const button = screen.getByTestId("confirm-registration-button");
    fireEvent.click(button);
    fireEvent.click(button);

    await waitFor(() => expect(confirm.mutateAsync).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByTestId("confirm-registration-title").textContent).toBe("Account confirmed"),
    );
  });

  it("shows the server's error when confirmation fails", async () => {
    confirm.mutateAsync.mockRejectedValue(new Error("Registration link has expired or is invalid"));
    render(<Page />);

    fireEvent.click(screen.getByTestId("confirm-registration-button"));

    await waitFor(() =>
      expect(screen.getByTestId("confirm-registration-error").textContent).toBe(
        "Registration link has expired or is invalid",
      ),
    );
    expect(screen.queryByTestId("confirm-registration-button")).toBeNull();
  });
});
