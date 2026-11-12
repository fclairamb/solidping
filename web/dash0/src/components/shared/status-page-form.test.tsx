/**
 * @vitest-environment jsdom
 *
 * Saving a status page with a slug the server rejects (400 VALIDATION_ERROR
 * with a `slug` field) used to let the ApiError escape the form's submit
 * handler as an unhandled rejection: no toast, no inline error. The form must
 * now catch it and show the server's message under the slug field.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import "@/i18n";
import { ApiError } from "@/api/client";
import type { StatusPage } from "@/api/hooks";
import { StatusPageForm } from "./status-page-form";

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

// jsdom has no ResizeObserver; the Radix Select inside the form measures itself.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const page = {
  uid: "00000000-0000-0000-0000-000000000001",
  name: "Acme status",
  slug: "acme-status",
  visibility: "public",
  isDefault: false,
  enabled: true,
} as unknown as StatusPage;

function renderEdit(onSubmit: () => Promise<void>) {
  render(
    <StatusPageForm mode="edit" initialData={page} isPending={false} onSubmit={onSubmit} onCancel={() => {}} />
  );
  fireEvent.submit(screen.getByTestId("status-page-slug-input").closest("form")!);
}

describe("StatusPageForm submit errors", () => {
  it("shows the server's slug message inline and toasts, without rethrowing", async () => {
    const msg = "Slug must start with a lowercase letter";
    const onSubmit = vi.fn().mockRejectedValue(
      new ApiError("Invalid slug format", "VALIDATION_ERROR", undefined, 400, undefined, [
        { name: "slug", message: msg },
      ])
    );
    renderEdit(onSubmit);

    expect(await screen.findByTestId("status-page-slug-error")).toHaveProperty("textContent", msg);
    expect(toastError).toHaveBeenCalledWith("Invalid slug format");

    // Editing the slug clears the stale server message.
    fireEvent.change(screen.getByTestId("status-page-slug-input"), { target: { value: "acme-two" } });
    expect(screen.queryByTestId("status-page-slug-error")).toBeNull();
  });

  it("toasts a generic message for a non-API failure", async () => {
    renderEdit(vi.fn().mockRejectedValue(new Error("network down")));
    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId("status-page-slug-error")).toBeNull();
  });

  it("stays silent on a refused demo write (client.ts already toasted)", async () => {
    renderEdit(vi.fn().mockRejectedValue(new ApiError("Read only", "DEMO_READ_ONLY", undefined, 403)));
    await new Promise((r) => setTimeout(r, 0));
    expect(toastError).not.toHaveBeenCalled();
  });
});
