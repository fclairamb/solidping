import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Button, buttonVariants } from "./button";
import { Badge, badgeVariants } from "./badge";
import { Progress } from "./progress";
import { Stepper } from "./stepper";
import { Switch } from "./switch";
import { Checkbox } from "./checkbox";
import { Input } from "./input";
import { Textarea } from "./textarea";

// Spec 2026-09-24-01 (electric identity, primitives). These pin the class
// contract the design reference documents; the e2e suite
// (electric-identity-primitives.spec.ts) checks what the browser paints.

function classesOf(html: string): string[] {
  const m = /class="([^"]*)"/.exec(html);
  return m ? m[1].split(/\s+/) : [];
}

describe("Button, default variant", () => {
  const cls = buttonVariants().split(/\s+/);

  it("paints --primary-gradient over the bg-primary fallback, with white text", () => {
    expect(cls).toContain("bg-primary");
    expect(cls).toContain("bg-primary-gradient");
    expect(cls).toContain("text-gradient-foreground");
    expect(cls).not.toContain("bg-accent-gradient");
  });

  it("keeps the inset highlight and the tinted shadow, and lifts only under motion-safe", () => {
    expect(cls).toContain("inset-shadow-highlight");
    expect(cls).toContain("shadow-primary");
    expect(cls).toContain("hover:shadow-primary-hover");
    expect(cls).toContain("hover:brightness-105");
    expect(cls).toContain("motion-safe:hover:-translate-y-px");
    expect(cls.filter((c) => c.includes("translate") && !c.startsWith("motion-safe:"))).toEqual([]);
  });

  it("offsets its focus ring from the gradient", () => {
    expect(cls).toContain("focus-visible:ring-2");
    expect(cls).toContain("focus-visible:ring-offset-2");
    expect(cls).toContain("focus-visible:ring-offset-background");
  });

  it("survives cn(): the rendered button still carries both the fill and the gradient", () => {
    const html = renderToStaticMarkup(<Button>Save</Button>);
    expect(classesOf(html)).toEqual(expect.arrayContaining(["bg-primary", "bg-primary-gradient"]));
  });

  it("drops the gradient when a caller recolors it (AlertDialogAction's delete confirms)", () => {
    const html = renderToStaticMarkup(
      <Button className="bg-destructive text-white hover:bg-destructive/90">Delete</Button>,
    );
    const c = classesOf(html);
    expect(c).toContain("bg-destructive");
    expect(c).not.toContain("bg-primary-gradient");
    expect(c).not.toContain("bg-primary");
  });

  it("leaves the other variants flat", () => {
    for (const variant of ["destructive", "outline", "secondary", "ghost", "link"] as const) {
      expect(buttonVariants({ variant })).not.toMatch(/gradient/);
    }
    expect(buttonVariants({ variant: "destructive" })).toContain("bg-destructive");
    expect(buttonVariants({ variant: "link" })).toContain("text-primary");
  });
});

describe("Badge", () => {
  it("default variant uses the text-safe primary gradient with white text", () => {
    const cls = badgeVariants().split(/\s+/);
    expect(cls).toEqual(
      expect.arrayContaining(["bg-primary", "bg-primary-gradient", "text-gradient-foreground"]),
    );
    const html = renderToStaticMarkup(<Badge>New</Badge>);
    expect(classesOf(html)).toEqual(expect.arrayContaining(["bg-primary", "bg-primary-gradient"]));
  });

  it("status variants stay flat soft tints", () => {
    for (const variant of ["success", "warning", "destructive", "secondary", "outline"] as const) {
      expect(badgeVariants({ variant })).not.toMatch(/gradient/);
    }
    expect(badgeVariants({ variant: "success" })).toContain("bg-status-ok/15");
  });
});

function indicatorClasses(html: string): string[] {
  // The inner div (the fill) is the second class attribute.
  const all = [...html.matchAll(/class="([^"]*)"/g)];
  return all[1][1].split(/\s+/);
}

describe("Progress", () => {
  it("fills with the accent gradient over bg-primary", () => {
    const c = indicatorClasses(renderToStaticMarkup(<Progress value={3} max={10} />));
    expect(c).toEqual(expect.arrayContaining(["bg-primary", "bg-accent-gradient"]));
  });

  it("a full destructive bar is red with the gradient removed", () => {
    const c = indicatorClasses(renderToStaticMarkup(<Progress value={10} max={10} />));
    expect(c).toEqual(expect.arrayContaining(["bg-none", "bg-destructive"]));
    expect(c).not.toContain("bg-accent-gradient");
    expect(c).not.toContain("bg-primary");
  });

  it("an overflowing value is still red", () => {
    const c = indicatorClasses(renderToStaticMarkup(<Progress value={12} max={10} />));
    expect(c).toContain("bg-destructive");
    expect(c).not.toContain("bg-accent-gradient");
  });

  it("a flat indicatorClassName override replaces the gradient", () => {
    const c = indicatorClasses(
      renderToStaticMarkup(
        <Progress value={10} max={10} destructiveWhenFull={false} indicatorClassName="bg-status-warning" />,
      ),
    );
    expect(c).toContain("bg-status-warning");
    expect(c).not.toContain("bg-accent-gradient");
  });
});

describe("Stepper", () => {
  it("done dots and done connectors use the accent gradient; upcoming ones stay flat", () => {
    const html = renderToStaticMarkup(
      <Stepper steps={[{ label: "One" }, { label: "Two" }, { label: "Three" }]} current={2} />,
    );
    const classLists = [...html.matchAll(/class="([^"]*)"/g)].map((m) => m[1]);
    const gradient = classLists.filter((c) => c.includes("bg-accent-gradient"));
    // Step 1 is done: its dot and its connector to step 2.
    expect(gradient).toHaveLength(2);
    expect(gradient.every((c) => c.includes("bg-primary"))).toBe(true);
    expect(classLists.some((c) => c.includes("bg-border"))).toBe(true);
  });
});

describe("Switch and Checkbox", () => {
  it("switch paints the accent gradient only in the checked state", () => {
    const c = classesOf(renderToStaticMarkup(<Switch defaultChecked />));
    expect(c).toContain("data-[state=checked]:bg-primary");
    expect(c).toContain("data-[state=checked]:bg-accent-gradient");
    expect(c).toContain("data-[state=unchecked]:bg-input");
  });

  it("checkbox paints the accent gradient with a white glyph when checked", () => {
    const c = classesOf(renderToStaticMarkup(<Checkbox defaultChecked />));
    expect(c).toContain("data-[state=checked]:bg-accent-gradient");
    expect(c).toContain("data-[state=checked]:text-gradient-foreground");
  });
});

describe("text field focus", () => {
  it("input and textarea show border-ring plus a 3px ring at 25%", () => {
    for (const html of [renderToStaticMarkup(<Input />), renderToStaticMarkup(<Textarea />)]) {
      const c = classesOf(html);
      expect(c).toEqual(
        expect.arrayContaining([
          "focus-visible:border-ring",
          "focus-visible:ring-[3px]",
          "focus-visible:ring-ring/25",
        ]),
      );
    }
  });
});
