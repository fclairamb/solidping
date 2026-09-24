import { type ClassValue, clsx } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/**
 * tailwind-merge taught about the electric-identity gradient utilities
 * declared with `@utility` in index.css (bg-primary-gradient,
 * bg-accent-gradient, bg-hero-gradient, and the chrome's bg-sidebar-gradient,
 * bg-sidebar-active and bg-page-glow).
 *
 * Stock tailwind-merge reads any unknown `bg-<word>` as a background COLOR, so
 * `cn("bg-primary bg-primary-gradient")` would silently drop `bg-primary` —
 * the flat fill that must stay underneath the gradient. They get their own
 * group instead, with one-way conflicts:
 *   - a gradient token never removes a bg-<color> (the fallback fill survives);
 *   - a LATER bg-<color> (bg-destructive, bg-muted, bg-emerald-500, …),
 *     bg-none or bg-linear-* removes an earlier gradient token, so a caller
 *     that recolors a default Button / Badge / Progress through className
 *     really gets the flat color it asked for rather than the gradient
 *     painted over it.
 * This only helps class lists that go through cn(). A raw class string still
 * needs an explicit bg-none to drop a gradient (see the design reference).
 */
const twMerge = extendTailwindMerge<"bg-gradient-token">({
  extend: {
    classGroups: {
      "bg-gradient-token": [
        {
          bg: [
            "primary-gradient",
            "accent-gradient",
            "hero-gradient",
            "sidebar-gradient",
            "sidebar-active",
            "page-glow",
          ],
        },
      ],
    },
    conflictingClassGroups: {
      "bg-gradient-token": ["bg-image"],
      "bg-image": ["bg-gradient-token"],
      "bg-color": ["bg-gradient-token"],
    },
    theme: {
      "inset-shadow": ["highlight"],
    },
  },
});

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9-]/g, "-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 100);
}
