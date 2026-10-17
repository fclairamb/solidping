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
import { Select, SelectTrigger, SelectValue } from "./select";

// The flat primitives: one solid --primary-fill for the primary action and
// the "on" states, no gradient, no tinted shadow, no hover movement. These
// pin the class contract the design reference documents; the e2e suite
// (electric-identity-primitives.spec.ts) checks what the browser paints.

function classesOf(html: string): string[] {
  const m = /class="([^"]*)"/.exec(html);
  return m ? m[1].split(/\s+/) : [];
}

/** Every class list in the markup, flattened. */
function allClasses(html: string): string[] {
  return [...html.matchAll(/class="([^"]*)"/g)].flatMap((m) => m[1].split(/\s+/));
}

const BUTTON_VARIANTS = ["default", "brand", "destructive", "outline", "secondary", "ghost", "link"] as const;

describe("Button, default variant", () => {
  const cls = buttonVariants().split(/\s+/);

  it("is a solid --primary-fill with its white label and a hairline shadow", () => {
    expect(cls).toEqual(
      expect.arrayContaining([
        "bg-primary-fill",
        "text-primary-fill-foreground",
        "shadow-xs",
        "hover:bg-primary-fill/90",
        "rounded-md",
      ]),
    );
    // Not the old text-blue fill underneath a gradient.
    expect(cls).not.toContain("bg-primary");
    expect(cls).not.toContain("rounded-lg");
  });

  it("offsets its focus ring from the fill", () => {
    expect(cls).toContain("focus-visible:ring-2");
    expect(cls).toContain("focus-visible:ring-offset-2");
    expect(cls).toContain("focus-visible:ring-offset-background");
  });

  it("animates colors only, and cancels a leading icon's legacy mr-2", () => {
    expect(cls).toContain("transition-colors");
    expect(cls).not.toContain("transition");
    expect(cls).toContain("gap-2");
    expect(cls).toContain("[&>svg]:mr-0");
  });

  it("survives cn(): the rendered button carries the fill and its label color", () => {
    const html = renderToStaticMarkup(<Button>Save</Button>);
    expect(classesOf(html)).toEqual(
      expect.arrayContaining(["bg-primary-fill", "text-primary-fill-foreground", "hover:bg-primary-fill/90"]),
    );
  });

  it("a caller recolor via className replaces the fill (cn)", () => {
    const html = renderToStaticMarkup(
      <Button className="bg-destructive text-white hover:bg-destructive/90">Delete</Button>,
    );
    const c = classesOf(html);
    expect(c).toEqual(expect.arrayContaining(["bg-destructive", "text-white", "hover:bg-destructive/90"]));
    expect(c).not.toContain("bg-primary-fill");
    expect(c).not.toContain("text-primary-fill-foreground");
    expect(c).not.toContain("hover:bg-primary-fill/90");
  });
});

describe("Button, every variant is flat", () => {
  it.each(BUTTON_VARIANTS)("%s: no gradient, no tinted or inset shadow, no translate, no brightness", (variant) => {
    const cls = buttonVariants({ variant }).split(/\s+/);
    expect(cls.filter((c) => /gradient|translate|brightness|inset-shadow/.test(c))).toEqual([]);
    // At most the hairline shadow-xs; never a colored elevation.
    expect(cls.filter((c) => /(^|:)shadow/.test(c) && c !== "shadow-xs")).toEqual([]);
  });

  it.each(BUTTON_VARIANTS)("%s: hover changes the color only", (variant) => {
    const hover = buttonVariants({ variant })
      .split(/\s+/)
      .filter((c) => c.includes("hover:"));
    for (const c of hover) {
      expect(c).toMatch(/^(dark:)?hover:(bg|text)-|^hover:underline$/);
    }
  });

  it("keeps the hairline shadow on the filled and outlined variants only", () => {
    for (const variant of ["default", "brand", "destructive", "outline"] as const) {
      expect(buttonVariants({ variant }).split(/\s+/), variant).toContain("shadow-xs");
    }
    for (const variant of ["secondary", "ghost", "link"] as const) {
      expect(buttonVariants({ variant }).split(/\s+/), variant).not.toContain("shadow-xs");
    }
    expect(buttonVariants({ variant: "link" })).toContain("text-primary");
  });

  it("every size keeps the rounded-md corner", () => {
    for (const size of ["default", "sm", "lg", "icon"] as const) {
      const cls = buttonVariants({ size }).split(/\s+/);
      expect(cls, size).toContain("rounded-md");
      expect(cls, size).not.toContain("rounded-lg");
    }
  });

  it("destructive: a solid red with the white label token, darker fill in dark mode (spec 2026-09-24-07)", () => {
    const cls = buttonVariants({ variant: "destructive" }).split(/\s+/);
    expect(cls).toEqual(
      expect.arrayContaining([
        "bg-destructive",
        "text-destructive-foreground",
        "shadow-xs",
        "hover:bg-destructive/90",
        "dark:bg-destructive/80",
        "dark:hover:bg-destructive/70",
      ]),
    );
    expect(cls).not.toContain("text-white");
  });
});

describe("Badge", () => {
  it("default variant is a soft primary tint with colored text", () => {
    const cls = badgeVariants().split(/\s+/);
    expect(cls).toEqual(expect.arrayContaining(["border-primary/25", "bg-primary/10", "text-primary"]));
    expect(cls.filter((c) => /gradient|primary-fill|^shadow/.test(c))).toEqual([]);
    const html = renderToStaticMarkup(<Badge>New</Badge>);
    expect(classesOf(html)).toEqual(expect.arrayContaining(["border-primary/25", "bg-primary/10", "text-primary"]));
  });

  it("status variants stay flat soft tints", () => {
    for (const variant of ["default", "success", "warning", "destructive", "secondary", "outline"] as const) {
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
  it("fills with the solid --primary-fill", () => {
    const c = indicatorClasses(renderToStaticMarkup(<Progress value={3} max={10} />));
    expect(c).toContain("bg-primary-fill");
    expect(c).not.toContain("bg-primary");
    expect(c.filter((cls) => cls.includes("gradient"))).toEqual([]);
  });

  it("a full destructive bar is solid red", () => {
    const c = indicatorClasses(renderToStaticMarkup(<Progress value={10} max={10} />));
    expect(c).toContain("bg-destructive");
    expect(c).not.toContain("bg-primary-fill");
  });

  it("an overflowing value is still red", () => {
    const c = indicatorClasses(renderToStaticMarkup(<Progress value={12} max={10} />));
    expect(c).toContain("bg-destructive");
    expect(c).not.toContain("bg-primary-fill");
  });

  it("a full bar stays blue when destructiveWhenFull is off", () => {
    const c = indicatorClasses(
      renderToStaticMarkup(<Progress value={10} max={10} destructiveWhenFull={false} />),
    );
    expect(c).toContain("bg-primary-fill");
    expect(c).not.toContain("bg-destructive");
  });

  it("an indicatorClassName override replaces the fill through cn()", () => {
    const c = indicatorClasses(
      renderToStaticMarkup(
        <Progress value={10} max={10} destructiveWhenFull={false} indicatorClassName="bg-status-warning" />,
      ),
    );
    expect(c).toContain("bg-status-warning");
    expect(c).not.toContain("bg-primary-fill");
  });
});

describe("Stepper", () => {
  it("done dots and done connectors use the solid fill; upcoming ones stay flat", () => {
    const html = renderToStaticMarkup(
      <Stepper steps={[{ label: "One" }, { label: "Two" }, { label: "Three" }]} current={2} />,
    );
    const classLists = [...html.matchAll(/class="([^"]*)"/g)].map((m) => m[1].split(/\s+/));
    const filled = classLists.filter((c) => c.includes("bg-primary-fill"));
    // Step 1 is done: its dot (with the white glyph) and its connector to step 2.
    expect(filled).toHaveLength(2);
    expect(filled.filter((c) => c.includes("text-primary-fill-foreground"))).toHaveLength(1);
    expect(classLists.some((c) => c.includes("bg-border"))).toBe(true);
    expect(allClasses(html).filter((c) => c.includes("gradient"))).toEqual([]);
  });
});

describe("Switch and Checkbox", () => {
  it("switch paints the solid fill only in the checked state", () => {
    const c = classesOf(renderToStaticMarkup(<Switch defaultChecked />));
    expect(c).toContain("data-[state=checked]:bg-primary-fill");
    expect(c).toContain("data-[state=unchecked]:bg-input");
    expect(c).not.toContain("data-[state=checked]:bg-primary");
    expect(c.filter((cls) => cls.includes("gradient"))).toEqual([]);
  });

  it("checkbox paints the solid fill edge to edge with a white glyph when checked", () => {
    const c = classesOf(renderToStaticMarkup(<Checkbox defaultChecked />));
    expect(c).toEqual(
      expect.arrayContaining([
        "data-[state=checked]:border-primary-fill",
        "data-[state=checked]:bg-primary-fill",
        "data-[state=checked]:text-primary-fill-foreground",
      ]),
    );
    expect(c).not.toContain("data-[state=checked]:border-transparent");
    expect(c.filter((cls) => cls.includes("gradient"))).toEqual([]);
  });
});

describe("text field focus", () => {
  it("input, textarea and the select trigger show border-ring plus a 3px ring at 25%", () => {
    const trigger = renderToStaticMarkup(
      <Select>
        <SelectTrigger aria-label="Region">
          <SelectValue placeholder="Pick one" />
        </SelectTrigger>
      </Select>,
    );
    expect(trigger).toContain('role="combobox"');
    for (const html of [renderToStaticMarkup(<Input />), renderToStaticMarkup(<Textarea />), trigger]) {
      const c = classesOf(html);
      expect(c).toEqual(
        expect.arrayContaining([
          "focus-visible:border-ring",
          "focus-visible:ring-[3px]",
          "focus-visible:ring-ring/25",
        ]),
      );
      // The old flush 1px ring (and the select's offset focus:ring-2) is gone.
      expect(c).not.toContain("focus-visible:ring-1");
      expect(c).not.toContain("focus:ring-2");
    }
  });
});
