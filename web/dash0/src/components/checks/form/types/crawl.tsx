// Website crawl check form (spec 2026-10-03-03). The essentials (start URL,
// page budget, what to check) are always visible; the crawl tuning (paths,
// pacing, run deadline, what makes it down) folds into the shared Advanced
// section through `CrawlAdvancedFields`.
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getFieldError } from "@/hooks/use-check-validation";
import { CRAWL_FINDING_TYPES } from "@/lib/crawl-findings";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField, validationMessage } from "./common";

const MAX_PAGES = 2000;
const DEFAULT_MAX_PAGES = 200;
const MIN_RUN_MINUTES = 5;
const MAX_RUN_MINUTES = 120;
const DEFAULT_FAIL_ON = ["broken_link", "mixed_content_active", "sitemap_error"];

export type SitemapMode = "auto" | "off" | "custom";

export interface CrawlState {
  url: string;
  maxPages: string;
  checkExternalLinks: boolean;
  checkMixedContent: boolean;
  respectRobots: boolean;
  sitemapMode: SitemapMode;
  sitemapUrl: string;
  include: string;
  exclude: string;
  concurrency: string;
  delayMs: string;
  maxRunMinutes: string;
  failOn: string[];
}

const lines = (text: string): string[] =>
  text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");

const asList = (value: unknown): string[] =>
  Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];

/** "30m" / "1h30m" / "45m0s" → minutes, "" when unset or unreadable. */
export function durationToMinutes(value: string): string {
  if (!value) return "";
  const match = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(value);
  if (!match || match[0] === "") return "";
  const minutes = Number(match[1] ?? 0) * 60 + Number(match[2] ?? 0) + Math.round(Number(match[3] ?? 0) / 60);
  return minutes > 0 ? String(minutes) : "";
}

function sitemapModeOf(value: string): SitemapMode {
  if (value === "" || value === "auto") return "auto";
  if (value === "off") return "off";
  return "custom";
}

export const crawlModule: CheckTypeModule<CrawlState> = {
  types: ["crawl"],
  ownedKeys: [
    "url",
    "maxPages",
    "checkExternalLinks",
    "checkMixedContent",
    "respectRobots",
    "sitemap",
    "include",
    "exclude",
    "concurrency",
    "delayMs",
    "maxRunDuration",
    "failOn",
  ],
  fromConfig: (config) => {
    const sitemap = getConfigField(config, "sitemap");
    const failOn = asList(config.failOn);
    return {
      url: getConfigField(config, "url"),
      maxPages: getConfigField(config, "maxPages"),
      checkExternalLinks: getConfigField(config, "checkExternalLinks") !== "false",
      checkMixedContent: getConfigField(config, "checkMixedContent") !== "false",
      respectRobots: getConfigField(config, "respectRobots") !== "false",
      sitemapMode: sitemapModeOf(sitemap),
      sitemapUrl: sitemapModeOf(sitemap) === "custom" ? sitemap : "",
      include: asList(config.include).join("\n"),
      exclude: asList(config.exclude).join("\n"),
      concurrency: getConfigField(config, "concurrency"),
      delayMs: getConfigField(config, "delayMs"),
      maxRunMinutes: durationToMinutes(getConfigField(config, "maxRunDuration")),
      failOn: failOn.length > 0 ? failOn : DEFAULT_FAIL_ON,
    };
  },
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    const errors: FieldErrors = [];

    if (state.url) cfg.url = state.url;
    else errors.push({ name: "url", message: validationMessage("urlRequired") });

    if (state.maxPages) {
      const pages = Number(state.maxPages);
      cfg.maxPages = pages;
      if (!Number.isInteger(pages) || pages < 1 || pages > MAX_PAGES) {
        errors.push({ name: "maxPages", message: validationMessage("crawlMaxPagesRange", { max: MAX_PAGES }) });
      }
    }

    // Defaults are on: only an explicit false is written.
    if (!state.checkExternalLinks) cfg.checkExternalLinks = false;
    if (!state.checkMixedContent) cfg.checkMixedContent = false;
    if (!state.respectRobots) cfg.respectRobots = false;

    if (state.sitemapMode === "off") cfg.sitemap = "off";
    if (state.sitemapMode === "custom" && state.sitemapUrl) cfg.sitemap = state.sitemapUrl;

    const include = lines(state.include);
    const exclude = lines(state.exclude);
    if (include.length > 0) cfg.include = include;
    if (exclude.length > 0) cfg.exclude = exclude;
    if (state.concurrency) cfg.concurrency = Number(state.concurrency);
    if (state.delayMs !== "") cfg.delayMs = Number(state.delayMs);

    if (state.maxRunMinutes) {
      const minutes = Number(state.maxRunMinutes);
      cfg.maxRunDuration = `${minutes}m`;
      if (!Number.isInteger(minutes) || minutes < MIN_RUN_MINUTES || minutes > MAX_RUN_MINUTES) {
        errors.push({
          name: "maxRunDuration",
          message: validationMessage("crawlMaxRunRange", { min: MIN_RUN_MINUTES, max: MAX_RUN_MINUTES }),
        });
      }
    }

    const failOn = [...state.failOn].sort();
    if (failOn.join(",") !== [...DEFAULT_FAIL_ON].sort().join(",")) cfg.failOn = state.failOn;

    return { config: cfg, errors };
  },
  Fields: CrawlFields,
};

function FieldError({ errors, name }: { errors: FieldErrors; name: string }) {
  const message = getFieldError(errors, name);
  return message ? <p className="text-xs text-destructive">{message}</p> : null;
}

function CrawlFields({ state, onChange, errors }: CheckTypeFieldsProps<CrawlState>) {
  const { t } = useTranslation("checks");
  const toggle = (key: "checkExternalLinks" | "checkMixedContent" | "respectRobots", label: string, help?: string) => (
    <div className="space-y-1">
      <label className="flex min-h-9 items-center gap-2">
        <Checkbox
          checked={state[key]}
          onCheckedChange={(v) => onChange({ ...state, [key]: v === true })}
          data-testid={`check-crawl-${key}-checkbox`}
        />
        <span className="text-sm">{label}</span>
      </label>
      {help && <p className="text-xs text-muted-foreground">{help}</p>}
    </div>
  );

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="crawlUrl">{t("crawl.url")}</Label>
        <Input
          id="crawlUrl"
          type="url"
          placeholder="https://www.acme.com/"
          value={state.url}
          onChange={(e) => onChange({ ...state, url: e.target.value })}
          className={cn(getFieldError(errors, "url") && "border-destructive")}
          data-testid="check-crawl-url-input"
        />
        <p className="text-xs text-muted-foreground">{t("crawl.urlHelp")}</p>
        <FieldError errors={errors} name="url" />
      </div>
      <div className="space-y-2">
        <Label htmlFor="crawlMaxPages">{t("crawl.maxPages")}</Label>
        <Input
          id="crawlMaxPages"
          type="number"
          min={1}
          max={MAX_PAGES}
          placeholder={String(DEFAULT_MAX_PAGES)}
          value={state.maxPages}
          onChange={(e) => onChange({ ...state, maxPages: e.target.value })}
          className={cn("w-full sm:w-40", getFieldError(errors, "maxPages") && "border-destructive")}
          data-testid="check-crawl-max-pages-input"
        />
        <p className="text-xs text-muted-foreground">{t("crawl.maxPagesHelp")}</p>
        <FieldError errors={errors} name="maxPages" />
      </div>
      {toggle("checkExternalLinks", t("crawl.checkExternalLinks"))}
      {toggle("checkMixedContent", t("crawl.checkMixedContent"))}
      {toggle("respectRobots", t("crawl.respectRobots"), t("crawl.respectRobotsHelp"))}
      <div className="space-y-2">
        <Label htmlFor="crawlSitemap">{t("crawl.sitemap")}</Label>
        <Select
          value={state.sitemapMode}
          onValueChange={(v) => onChange({ ...state, sitemapMode: v as SitemapMode })}
        >
          <SelectTrigger id="crawlSitemap" className="w-full sm:w-80" data-testid="check-crawl-sitemap-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="auto">{t("crawl.sitemapAuto")}</SelectItem>
            <SelectItem value="off">{t("crawl.sitemapOff")}</SelectItem>
            <SelectItem value="custom">{t("crawl.sitemapCustom")}</SelectItem>
          </SelectContent>
        </Select>
        {state.sitemapMode === "custom" && (
          <Input
            type="url"
            aria-label={t("crawl.sitemapUrl")}
            placeholder="https://www.acme.com/sitemap.xml"
            value={state.sitemapUrl}
            onChange={(e) => onChange({ ...state, sitemapUrl: e.target.value })}
            data-testid="check-crawl-sitemap-url-input"
          />
        )}
      </div>
    </>
  );
}

/** Crawl tuning, rendered inside the form's Advanced section. */
export function CrawlAdvancedFields({ state, onChange, errors }: CheckTypeFieldsProps<CrawlState>) {
  const { t } = useTranslation("checks");
  const toggleFailOn = (type: string, on: boolean) =>
    onChange({
      ...state,
      failOn: on ? [...state.failOn.filter((x) => x !== type), type] : state.failOn.filter((x) => x !== type),
    });

  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="crawlInclude">{t("crawl.include")}</Label>
          <Textarea
            id="crawlInclude"
            rows={3}
            placeholder={"/blog\n/docs"}
            value={state.include}
            onChange={(e) => onChange({ ...state, include: e.target.value })}
            data-testid="check-crawl-include-input"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="crawlExclude">{t("crawl.exclude")}</Label>
          <Textarea
            id="crawlExclude"
            rows={3}
            placeholder={"/admin\n/*/print"}
            value={state.exclude}
            onChange={(e) => onChange({ ...state, exclude: e.target.value })}
            data-testid="check-crawl-exclude-input"
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">{t("crawl.patternsHelp")}</p>
      <div className="grid gap-4 sm:grid-cols-3">
        <div className="space-y-2">
          <Label htmlFor="crawlConcurrency">{t("crawl.concurrency")}</Label>
          <Input
            id="crawlConcurrency"
            type="number"
            min={1}
            max={4}
            placeholder="2"
            value={state.concurrency}
            onChange={(e) => onChange({ ...state, concurrency: e.target.value })}
            data-testid="check-crawl-concurrency-input"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="crawlDelay">{t("crawl.delayMs")}</Label>
          <Input
            id="crawlDelay"
            type="number"
            min={0}
            max={5000}
            placeholder="250"
            value={state.delayMs}
            onChange={(e) => onChange({ ...state, delayMs: e.target.value })}
            data-testid="check-crawl-delay-input"
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="crawlMaxRun">{t("crawl.maxRunDuration")}</Label>
          <Input
            id="crawlMaxRun"
            type="number"
            min={MIN_RUN_MINUTES}
            max={MAX_RUN_MINUTES}
            placeholder="30"
            value={state.maxRunMinutes}
            onChange={(e) => onChange({ ...state, maxRunMinutes: e.target.value })}
            className={cn(getFieldError(errors, "maxRunDuration") && "border-destructive")}
            data-testid="check-crawl-max-run-input"
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">{t("crawl.maxRunDurationHelp")}</p>
      <FieldError errors={errors} name="maxRunDuration" />
      <div className="space-y-2">
        <Label>{t("crawl.failOn")}</Label>
        <div className="grid gap-1 sm:grid-cols-2">
          {CRAWL_FINDING_TYPES.map((type) => (
            <label key={type} className="flex min-h-9 items-center gap-2">
              <Checkbox
                checked={state.failOn.includes(type)}
                onCheckedChange={(v) => toggleFailOn(type, v === true)}
                data-testid={`check-crawl-fail-on-${type}`}
              />
              <span className="text-sm">{t(`crawl.finding.${type}`)}</span>
            </label>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">{t("crawl.failOnHelp")}</p>
      </div>
    </>
  );
}

/** One-line summary of the crawl tuning for the collapsed Advanced section. */
export function crawlAdvancedSummary(state: CrawlState): { text: string; customized: boolean } {
  const customized =
    state.include !== "" ||
    state.exclude !== "" ||
    state.concurrency !== "" ||
    state.delayMs !== "" ||
    state.maxRunMinutes !== "" ||
    [...state.failOn].sort().join(",") !== [...DEFAULT_FAIL_ON].sort().join(",");
  const text = `${state.concurrency || "2"}× · ${state.delayMs || "250"} ms · ${state.maxRunMinutes || "30"} min`;
  return { text, customized };
}
