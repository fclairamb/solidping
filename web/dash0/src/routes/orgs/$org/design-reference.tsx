// Dev-facing component reference. All in-page labels are intentionally
// hardcoded English — do not add i18n keys for the showcase content.
// Only the sidebar entry (nav:designReference) is translated.

import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { ArrowRight, Check, Copy, Moon, Palette, Sun } from "lucide-react";

import { PageHeader } from "@/components/shared/page-header";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";

export const Route = createFileRoute("/orgs/$org/design-reference")({
  component: DesignReferencePage,
});

const SECTIONS: { id: string; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "page-header", label: "Page header" },
  { id: "breadcrumbs", label: "Breadcrumbs" },
  { id: "color-tokens", label: "Color tokens" },
  { id: "buttons-badges", label: "Buttons & badges" },
  { id: "forms", label: "Forms" },
  { id: "data-display", label: "Data display" },
  { id: "feedback", label: "Feedback" },
];

function DesignReferencePage() {
  return (
    <div className="space-y-8 pb-24">
      <PageHeader
        icon={Palette}
        title="Design Reference"
        description="A live, scrollable index of every dash0 UI primitive and shared pattern. Copy the import line next to each example. Toggle the theme to verify dark-mode parity. Source: web/dash0/src/routes/orgs/$org/design-reference.tsx."
      />
      <SubNav />
      <OverviewSection />
      <PageHeaderSection />
      <BreadcrumbsSection />
      <ColorTokensSection />
      <ButtonsBadgesSection />
      <FormsSection />
    </div>
  );
}

function SubNav() {
  return (
    <nav
      aria-label="Design reference sections"
      className="sticky top-0 z-10 -mx-3 border-b bg-background/80 px-3 py-2 backdrop-blur sm:-mx-4 sm:px-4"
    >
      <ul className="flex flex-wrap gap-2">
        {SECTIONS.map((s) => (
          <li key={s.id}>
            <a
              href={`#${s.id}`}
              className="inline-flex items-center rounded-full border border-border bg-card px-3 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
            >
              {s.label}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}

export function Section({
  id,
  title,
  description,
  children,
}: {
  id: string;
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <section id={id} className="scroll-mt-20 space-y-4">
      <header>
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        {description ? (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        ) : null}
      </header>
      {children}
    </section>
  );
}

export function CodeSnippet({ code }: { code: string }) {
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API requires a secure context; ignore failures silently
      // — the page still shows the code so contributors can copy by hand.
    }
  };

  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-md border bg-muted/40 px-3 py-2 pr-12 text-xs leading-relaxed">
        <code>{code}</code>
      </pre>
      <button
        type="button"
        onClick={onCopy}
        aria-label={copied ? "Copied" : "Copy to clipboard"}
        className="absolute right-1.5 top-1.5 inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
      >
        {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
      </button>
    </div>
  );
}

export function ExampleRow({
  preview,
  importLine,
}: {
  preview: React.ReactNode;
  importLine: string;
}) {
  return (
    <div className="grid gap-3 rounded-md border bg-card p-4 md:grid-cols-[1fr_minmax(0,1fr)] md:items-start">
      <div className="flex flex-wrap items-center gap-2">{preview}</div>
      <CodeSnippet code={importLine} />
    </div>
  );
}

function OverviewSection() {
  return (
    <Section
      id="overview"
      title="Overview"
      description="Single-page reference for every shipped UI primitive and shared pattern. Every example renders live; every import line copies clean. Toggle the theme below to verify dark-mode parity without leaving the page."
    >
      <div className="rounded-md border bg-card p-4">
        <PageThemeToggle />
        <p className="mt-3 text-sm text-muted-foreground">
          Tip: open <code className="rounded bg-muted px-1 py-0.5 text-xs">web/dash0/src/routes/orgs/$org/design-reference.tsx</code> when copying patterns — many examples here lift directly from real routes (e.g. <code className="rounded bg-muted px-1 py-0.5 text-xs">checks.index.tsx</code>) and the source file is the canonical reference.
        </p>
      </div>
    </Section>
  );
}

function PageThemeToggle() {
  const isDark = () =>
    typeof document !== "undefined" &&
    document.documentElement.classList.contains("dark");
  const [dark, setDark] = useState<boolean>(isDark);

  const toggle = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
    try {
      localStorage.setItem("theme", next ? "dark" : "light");
    } catch {
      // Storage may be unavailable (private mode); the visual toggle still works.
    }
  };

  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <p className="text-sm font-medium">Theme</p>
        <p className="text-xs text-muted-foreground">
          Toggles the same <code>html.dark</code> class the sidebar toggle uses.
        </p>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={toggle}
        aria-pressed={dark}
        aria-label={dark ? "Switch to light mode" : "Switch to dark mode"}
      >
        {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
        <span>{dark ? "Light mode" : "Dark mode"}</span>
      </Button>
    </div>
  );
}

function PageHeaderSection() {
  return (
    <Section
      id="page-header"
      title="Page header"
      description="The icon-on-left + title + optional description + right-aligned actions pattern. Use it for new pages; existing inline headers will adopt it over time."
    >
      <div className="rounded-md border bg-card p-4">
        <PageHeader
          icon={Palette}
          title="Example page title"
          description="A short subtitle that explains what the page is for."
          actions={<Button size="sm">Primary action</Button>}
        />
      </div>
      <CodeSnippet code={`import { PageHeader } from "@/components/shared/page-header";`} />
    </Section>
  );
}

function BreadcrumbsSection() {
  const snippet = `// In web/dash0/src/routes/orgs/$org.tsx, add a section flag and a branch
const isFooBar = matches.some((m) => m.routeId.startsWith("/orgs/$org/foo-bar"));

if (isFooBar) {
  return (
    <span className={activeClass}>
      <FooBarIcon className={iconClass} />
      Foo Bar
    </span>
  );
}`;
  return (
    <Section
      id="breadcrumbs"
      title="Breadcrumbs"
      description="Breadcrumbs are route-driven, not a drop-in component. They live in the Breadcrumbs() function inside web/dash0/src/routes/orgs/$org.tsx. Each section adds a flag (isChecks, isIncidents, …) and a branch. The header at the top of this very page is a live example."
    >
      <div className="rounded-md border bg-card p-4">
        <p className="text-sm text-muted-foreground">
          Look at the header bar above the sidebar trigger — the breadcrumb shows{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">Design Reference</code>{" "}
          with the Palette icon. That branch was added alongside the others in{" "}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">$org.tsx</code>.
        </p>
      </div>
      <CodeSnippet code={snippet} />
    </Section>
  );
}

const COLOR_TOKENS: { name: string; varName: string; description?: string }[] = [
  { name: "primary", varName: "--primary", description: "Brand blue, primary actions" },
  { name: "destructive", varName: "--destructive", description: "Errors, destructive actions" },
  { name: "accent", varName: "--accent", description: "Hover/highlight surface" },
  { name: "muted-foreground", varName: "--muted-foreground", description: "Secondary text" },
  { name: "status-ok", varName: "--status-ok", description: "Healthy / passing" },
  { name: "status-warning", varName: "--status-warning", description: "Degraded" },
  { name: "status-error", varName: "--status-error", description: "Failing" },
];

const CHART_TOKENS = ["--chart-1", "--chart-2", "--chart-3", "--chart-4", "--chart-5"];

function Swatch({ varName, label, description }: { varName: string; label: string; description?: string }) {
  return (
    <div className="flex items-center gap-3 rounded-md border bg-card p-3">
      <div
        className="h-10 w-10 shrink-0 rounded-md border"
        style={{ backgroundColor: `var(${varName})` }}
      />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium leading-tight">{label}</p>
        <p className="font-mono text-xs text-muted-foreground">{varName}</p>
        {description ? (
          <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
        ) : null}
      </div>
    </div>
  );
}

function ButtonsBadgesSection() {
  return (
    <Section
      id="buttons-badges"
      title="Buttons & badges"
      description="All variants and sizes shipped today. Pick the one with the least visual weight that still does the job."
    >
      <div className="space-y-4">
        <h3 className="text-sm font-medium">Button variants</h3>
        <ExampleRow
          preview={
            <>
              <Button>Default</Button>
              <Button variant="destructive">Destructive</Button>
              <Button variant="outline">Outline</Button>
              <Button variant="secondary">Secondary</Button>
              <Button variant="ghost">Ghost</Button>
              <Button variant="link">Link</Button>
            </>
          }
          importLine={`import { Button } from "@/components/ui/button";`}
        />

        <h3 className="text-sm font-medium">Button sizes</h3>
        <ExampleRow
          preview={
            <>
              <Button size="sm">Small</Button>
              <Button size="default">Default</Button>
              <Button size="lg">Large</Button>
              <Button size="icon" aria-label="Icon button">
                <ArrowRight />
              </Button>
            </>
          }
          importLine={`import { Button } from "@/components/ui/button";`}
        />

        <h3 className="text-sm font-medium">Badge variants</h3>
        <ExampleRow
          preview={
            <>
              <Badge>Default</Badge>
              <Badge variant="secondary">Secondary</Badge>
              <Badge variant="success">Success</Badge>
              <Badge variant="warning">Warning</Badge>
              <Badge variant="destructive">Destructive</Badge>
              <Badge variant="outline">Outline</Badge>
            </>
          }
          importLine={`import { Badge } from "@/components/ui/badge";`}
        />
      </div>
    </Section>
  );
}

function FormsSection() {
  return (
    <Section
      id="forms"
      title="Forms"
      description="Inputs, controls, and the canonical assembled form. Each field group uses space-y-2 internally; spacing between fields is space-y-4."
    >
      <div className="space-y-4">
        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-input">Input</Label>
              <Input id="dr-input" placeholder="A standard text input" />
            </div>
          }
          importLine={`import { Input } from "@/components/ui/input";\nimport { Label } from "@/components/ui/label";`}
        />

        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-textarea">Textarea</Label>
              <Textarea id="dr-textarea" placeholder="Multi-line input" rows={3} />
            </div>
          }
          importLine={`import { Textarea } from "@/components/ui/textarea";`}
        />

        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-password">Password input</Label>
              <PasswordInput id="dr-password" placeholder="••••••••" />
            </div>
          }
          importLine={`import { PasswordInput } from "@/components/ui/password-input";`}
        />

        <ExampleRow
          preview={
            <div className="w-full max-w-sm space-y-2">
              <Label htmlFor="dr-select">Select</Label>
              <Select>
                <SelectTrigger id="dr-select">
                  <SelectValue placeholder="Pick one" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="alpha">Alpha</SelectItem>
                  <SelectItem value="beta">Beta</SelectItem>
                  <SelectItem value="gamma">Gamma</SelectItem>
                </SelectContent>
              </Select>
            </div>
          }
          importLine={`import {\n  Select,\n  SelectContent,\n  SelectItem,\n  SelectTrigger,\n  SelectValue,\n} from "@/components/ui/select";`}
        />

        <ExampleRow
          preview={
            <div className="flex items-center gap-2">
              <Checkbox id="dr-checkbox" />
              <Label htmlFor="dr-checkbox">Checkbox label</Label>
            </div>
          }
          importLine={`import { Checkbox } from "@/components/ui/checkbox";`}
        />

        <ExampleRow
          preview={
            <div className="flex items-center gap-2">
              <Switch id="dr-switch" />
              <Label htmlFor="dr-switch">Switch label</Label>
            </div>
          }
          importLine={`import { Switch } from "@/components/ui/switch";`}
        />

        <h3 className="text-sm font-medium">Assembled form</h3>
        <div className="rounded-md border bg-card p-4">
          <form className="max-w-md space-y-4" onSubmit={(e) => e.preventDefault()}>
            <div className="space-y-2">
              <Label htmlFor="dr-name">Name</Label>
              <Input id="dr-name" placeholder="Acme Inc." />
              <p className="text-xs text-muted-foreground">
                Used as the display name across the dashboard.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="dr-email">Email</Label>
              <Input
                id="dr-email"
                type="email"
                placeholder="contact@acme.test"
                aria-invalid="true"
                aria-describedby="dr-email-error"
              />
              <p id="dr-email-error" className="text-xs text-destructive">
                This is what an inline error message looks like.
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="dr-notes">Notes</Label>
              <Textarea id="dr-notes" rows={3} />
            </div>
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline">
                Cancel
              </Button>
              <Button type="submit">Save</Button>
            </div>
          </form>
        </div>
      </div>
    </Section>
  );
}

function ColorTokensSection() {
  return (
    <Section
      id="color-tokens"
      title="Color tokens"
      description="Use the CSS variable token, not a hex. Tailwind classes like bg-primary, text-destructive, and bg-muted resolve to these tokens — they swap correctly in dark mode."
    >
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {COLOR_TOKENS.map((t) => (
          <Swatch key={t.varName} varName={t.varName} label={t.name} description={t.description} />
        ))}
      </div>
      <div className="space-y-2">
        <h3 className="text-sm font-medium">Chart palette</h3>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          {CHART_TOKENS.map((v, i) => (
            <Swatch key={v} varName={v} label={`chart-${i + 1}`} />
          ))}
        </div>
      </div>
    </Section>
  );
}
