import { describe, expect, it } from "vitest";
import { cn } from "./utils";

// The chrome's background-IMAGE utilities are declared with @utility in
// index.css. Stock tailwind-merge filed them under background COLOR, which
// silently dropped the flat fill kept underneath them.
describe("cn() and the background-image utilities", () => {
  it("keeps the flat fill underneath an image token", () => {
    // The regression this guards: stock twMerge returned only the gradient.
    expect(cn("bg-sidebar bg-sidebar-gradient")).toBe("bg-sidebar bg-sidebar-gradient");
    expect(cn("bg-card", "bg-sidebar bg-sidebar-gradient")).toBe(
      "bg-sidebar bg-sidebar-gradient",
    );
    expect(cn("bg-background bg-page-glow")).toBe("bg-background bg-page-glow");
  });

  it("drops the image when a later class sets a flat color, bg-none or another image", () => {
    expect(cn("bg-sidebar bg-sidebar-gradient", "bg-muted")).toBe("bg-muted");
    expect(cn("bg-page-glow", "bg-none")).toBe("bg-none");
    expect(cn("bg-sidebar bg-sidebar-gradient", "bg-linear-to-r")).toBe(
      "bg-sidebar bg-linear-to-r",
    );
  });

  it("only conflicts within the same variant", () => {
    expect(
      cn("hover:bg-sidebar-accent data-[active=true]:bg-sidebar-active"),
    ).toBe("hover:bg-sidebar-accent data-[active=true]:bg-sidebar-active");
  });

  it("leaves unrelated background classes alone", () => {
    // Positive control: the extension must not change ordinary merging.
    expect(cn("bg-muted", "bg-card")).toBe("bg-card");
    expect(cn("bg-muted bg-linear-to-r")).toBe("bg-muted bg-linear-to-r");
    expect(cn("bg-primary-fill", "bg-destructive")).toBe("bg-destructive");
  });
});
