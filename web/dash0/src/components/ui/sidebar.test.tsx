import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import {
  Sidebar,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
} from "./sidebar";
import { LiveStatusDot } from "@/components/layout/live-status-dot";

// Spec 2026-09-24-02: the sidebar is always dark navy. These pin the class
// contract; e2e electric-identity-app-chrome.spec.ts checks the painted
// colors on desktop, the icon rail and the mobile sheet.

const source = readFileSync(join(__dirname, "sidebar.tsx"), "utf8");

function classesAt(html: string, marker: string): string[] {
  const i = html.indexOf(marker);
  if (i < 0) throw new Error(`no ${marker} in the markup`);
  const tagStart = html.lastIndexOf("<", i);
  const tag = html.slice(tagStart, html.indexOf(">", i) + 1);
  const m = /class="([^"]*)"/.exec(tag);
  return m ? m[1].split(/\s+/) : [];
}

// SSR never runs useIsMobile's effect, so this is the desktop branch.
const html = renderToStaticMarkup(
  <SidebarProvider>
    <Sidebar>
      <SidebarGroup>
        <SidebarGroupLabel>Monitoring</SidebarGroupLabel>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton isActive>Dashboard</SidebarMenuButton>
          </SidebarMenuItem>
          <SidebarMenuItem>
            <SidebarMenuButton>Checks</SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarGroup>
    </Sidebar>
  </SidebarProvider>,
);

describe("Sidebar, always dark navy", () => {
  it("scopes the desktop root to the dark tokens", () => {
    expect(classesAt(html, 'data-slot="sidebar"')).toContain("dark");
  });

  it("paints the navy gradient over the solid navy fallback", () => {
    expect(classesAt(html, 'data-slot="sidebar-inner"')).toEqual(
      expect.arrayContaining(["bg-sidebar", "bg-sidebar-gradient"]),
    );
  });

  it("draws its right edge in --sidebar-border, not the page's --border", () => {
    expect(classesAt(html, 'data-slot="sidebar-container"')).toEqual(
      expect.arrayContaining(["border-sidebar-border", "group-data-[side=left]:border-r"]),
    );
  });

  it("labels groups with --sidebar-muted-foreground", () => {
    const label = classesAt(html, 'data-slot="sidebar-group-label"');
    expect(label).toContain("text-sidebar-muted-foreground");
    expect(label.filter((c) => c.startsWith("text-sidebar-foreground"))).toEqual([]);
  });

  it("marks the active item with the wash, semibold and a 3px cyan edge bar", () => {
    const active = classesAt(html, 'data-active="true"');
    expect(active).toEqual(
      expect.arrayContaining([
        "data-[active=true]:bg-sidebar-active",
        "data-[active=true]:font-semibold",
        "data-[active=true]:text-sidebar-accent-foreground",
        "data-[active=true]:before:absolute",
        "data-[active=true]:before:w-[3px]",
        "data-[active=true]:before:bg-sidebar-primary",
        "data-[active=true]:before:rounded-r-[3px]",
        "data-[active=true]:before:-left-2",
      ]),
    );
    // Hover stays the flat 5% wash, and the old pale active fill is gone.
    expect(active).toContain("hover:bg-sidebar-accent");
    expect(active).not.toContain("data-[active=true]:bg-sidebar-accent");
    // The bar must escape the button's overflow-hidden clip: its containing
    // block is the relative menu item, so the button must NOT be positioned.
    expect(active).toContain("overflow-hidden");
    expect(active.filter((c) => c === "relative" || c === "absolute")).toEqual([]);
    expect(classesAt(html, 'data-slot="sidebar-menu-item"')).toContain("relative");
  });

  it("gives the mobile sheet its own dark scope, navy surface and edge", () => {
    // The Sheet portals out of the desktop root, so it cannot inherit it.
    const sheet = /<SheetContent[\s\S]*?className="([^"]*)"/.exec(source)?.[1].split(/\s+/) ?? [];
    expect(sheet).toEqual(
      expect.arrayContaining([
        "dark",
        "bg-sidebar",
        "bg-sidebar-gradient",
        "border-sidebar-border",
        "dark:border-sidebar-border",
      ]),
    );
  });

  it("never wraps an oklch token in hsl()", () => {
    expect(source).not.toMatch(/hsl\(var\(--/);
  });
});

describe("LiveStatusDot on the navy", () => {
  it("draws its idle (connecting) state with a token, not a pale fixed gray", () => {
    // Outside LiveEventsProvider the status is "connecting". bg-gray-300
    // vanished on the navy; --muted-foreground resolves per surface.
    const dot = renderToStaticMarkup(
      <SidebarProvider>
        <LiveStatusDot />
      </SidebarProvider>,
    );
    expect(dot).toContain('data-status="connecting"');
    const c = classesAt(dot, 'data-testid="live-status-dot"');
    expect(c).toContain("bg-muted-foreground");
    expect(c).not.toContain("bg-gray-300");
  });
});
