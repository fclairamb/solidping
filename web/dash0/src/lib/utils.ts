import { type ClassValue, clsx } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/**
 * tailwind-merge taught about the chrome's background-IMAGE utilities declared
 * with `@utility` in index.css (bg-sidebar-gradient, bg-sidebar-active and
 * bg-page-glow).
 *
 * Stock tailwind-merge reads any unknown `bg-<word>` as a background COLOR, so
 * `cn("bg-sidebar bg-sidebar-gradient")` would silently drop `bg-sidebar` —
 * the flat fill that must stay underneath the image. They get their own
 * group instead, with one-way conflicts:
 *   - an image token never removes a bg-<color> (the fallback fill survives);
 *   - a LATER bg-<color>, bg-none or bg-linear-* removes an earlier image
 *     token, so a caller that recolors the element through className really
 *     gets the flat color it asked for.
 * This only helps class lists that go through cn().
 */
const twMerge = extendTailwindMerge<"bg-gradient-token">({
  extend: {
    classGroups: {
      "bg-gradient-token": [
        { bg: ["sidebar-gradient", "sidebar-active", "page-glow"] },
      ],
    },
    conflictingClassGroups: {
      "bg-gradient-token": ["bg-image"],
      "bg-image": ["bg-gradient-token"],
      "bg-color": ["bg-gradient-token"],
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
