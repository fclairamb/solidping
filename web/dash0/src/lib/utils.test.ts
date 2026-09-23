import { describe, expect, it } from "vitest";
import { cn } from "./utils";

// Spec 2026-09-24-01: the electric-identity gradient utilities are
// background-IMAGE classes declared with @utility in index.css. Stock
// tailwind-merge filed them under background COLOR, which silently dropped
// the bg-primary fill the default Button keeps underneath its gradient.
describe("cn() and the gradient utilities", () => {
  it("keeps the flat fill underneath a gradient token", () => {
    // The regression this guards: stock twMerge returned "bg-primary-gradient".
    expect(cn("bg-primary bg-primary-gradient")).toBe(
      "bg-primary bg-primary-gradient",
    );
    expect(cn("bg-primary bg-accent-gradient")).toBe(
      "bg-primary bg-accent-gradient",
    );
    expect(cn("bg-primary bg-hero-gradient")).toBe("bg-primary bg-hero-gradient");
  });

  it("drops the gradient when a later class sets a flat background color", () => {
    // How the 19 delete confirmations recolor AlertDialogAction (which is
    // cn(buttonVariants(), className)): they must stay red, not blue-over-red.
    expect(
      cn("bg-primary bg-primary-gradient", "bg-destructive hover:bg-destructive/90"),
    ).toBe("bg-destructive hover:bg-destructive/90");
    expect(cn("bg-primary bg-accent-gradient", "bg-status-warning")).toBe(
      "bg-status-warning",
    );
    expect(cn("bg-primary bg-accent-gradient", "bg-emerald-500")).toBe(
      "bg-emerald-500",
    );
  });

  it("drops the gradient for a later bg-none or another background image", () => {
    expect(cn("bg-accent-gradient", "bg-none bg-destructive")).toBe(
      "bg-none bg-destructive",
    );
    expect(cn("bg-primary bg-accent-gradient", "bg-linear-to-r")).toBe(
      "bg-primary bg-linear-to-r",
    );
    // The legacy v3 spelling (still painted by Tailwind v4) is filed under
    // background COLOR by tailwind-merge 3, so it replaces both — which is
    // what the onboarding checklist's progress bar relies on.
    expect(cn("bg-primary bg-accent-gradient", "bg-gradient-to-r")).toBe(
      "bg-gradient-to-r",
    );
    expect(cn("bg-linear-to-r", "bg-accent-gradient")).toBe("bg-accent-gradient");
  });

  it("only conflicts within the same variant", () => {
    // A hover color must not remove the resting gradient.
    expect(cn("bg-primary bg-primary-gradient", "hover:bg-primary/90")).toBe(
      "bg-primary bg-primary-gradient hover:bg-primary/90",
    );
    expect(
      cn(
        "data-[state=checked]:bg-primary data-[state=checked]:bg-accent-gradient",
        "data-[state=checked]:bg-status-ok",
      ),
    ).toBe("data-[state=checked]:bg-status-ok");
  });

  it("leaves unrelated background classes alone", () => {
    // Positive control: the extension must not change ordinary merging.
    expect(cn("bg-muted", "bg-card")).toBe("bg-card");
    expect(cn("bg-muted bg-linear-to-r")).toBe("bg-muted bg-linear-to-r");
    expect(cn("inset-shadow-highlight shadow-primary")).toBe(
      "inset-shadow-highlight shadow-primary",
    );
  });
});
