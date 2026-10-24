// Vendor status page check form (spec 2026-10-17-02). The vendor is picked from
// a searchable catalog or entered as a custom Statuspage v2 URL, then the
// components to watch are ticked (none = the whole page). The browser cannot
// fetch the vendor page itself (CSP), so the component list comes from the API.
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { cn } from "@/lib/utils";
import { getFieldError } from "@/hooks/use-check-validation";
import { apiFetch } from "@/api/client";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps } from "./common";
import { getConfigField, validationMessage } from "./common";
import { useCheckFormFields } from "./context";

export interface VendorStatusState {
  url: string;
  // Catalog slug, "" for a custom URL.
  provider: string;
  components: string[];
}

interface CatalogEntry {
  slug: string;
  name: string;
  url: string;
  category: string;
}

interface VendorComponent {
  id: string;
  name: string;
  group?: string;
}

const asList = (value: unknown): string[] =>
  Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];

function useCatalog() {
  return useQuery({
    queryKey: ["check-types", "vendor-status", "catalog"],
    queryFn: async () =>
      (await apiFetch<{ data: CatalogEntry[] }>("/api/v1/check-types/vendor-status/catalog")).data ?? [],
    staleTime: 60 * 60 * 1000,
  });
}

function useVendorComponents(org: string, url: string) {
  return useQuery({
    queryKey: ["check-types", "vendor-status", "components", org, url],
    queryFn: async () =>
      (
        await apiFetch<{ data: VendorComponent[] }>(`/api/v1/orgs/${org}/checks/vendor-status/components`, {
          method: "POST",
          body: JSON.stringify({ url }),
        })
      ).data ?? [],
    enabled: /^https?:\/\/[^/]+/i.test(url),
    retry: false,
    staleTime: 5 * 60 * 1000,
  });
}

function Fields({ state, onChange, errors }: CheckTypeFieldsProps<VendorStatusState>) {
  const { t } = useTranslation("checks");
  const { org } = useCheckFormFields();
  const catalog = useCatalog();
  const [search, setSearch] = useState("");
  // A stored check with no catalog slug is a custom URL.
  const [custom, setCustom] = useState(state.url !== "" && state.provider === "");
  const components = useVendorComponents(org, state.url.trim());

  const needle = search.trim().toLowerCase();
  const entries = (catalog.data ?? []).filter(
    (entry) => needle === "" || entry.name.toLowerCase().includes(needle) || entry.slug.includes(needle),
  );

  const pick = (entry: CatalogEntry) => {
    setCustom(false);
    onChange({ url: entry.url, provider: entry.slug, components: [] });
  };

  const toggle = (id: string, on: boolean) => {
    const next = on ? [...state.components, id] : state.components.filter((c) => c !== id);
    onChange({ ...state, components: next });
  };

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="vendor-status-search">{t("vendorStatus.vendor")}</Label>
        <Input
          id="vendor-status-search"
          type="search"
          placeholder={t("vendorStatus.search")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          data-testid="check-vendor-status-catalog-search"
        />
        <div
          className="flex max-h-56 flex-wrap gap-2 overflow-y-auto rounded-md border p-2"
          data-testid="check-vendor-status-catalog"
        >
          {entries.map((entry) => (
            <Button
              key={entry.slug}
              type="button"
              size="sm"
              variant={!custom && state.provider === entry.slug ? "default" : "outline"}
              onClick={() => pick(entry)}
              data-testid={`check-vendor-status-catalog-${entry.slug}`}
            >
              {entry.name}
            </Button>
          ))}
          <Button
            type="button"
            size="sm"
            variant={custom ? "default" : "outline"}
            onClick={() => {
              setCustom(true);
              onChange({ url: "", provider: "", components: [] });
            }}
            data-testid="check-vendor-status-custom"
          >
            {t("vendorStatus.custom")}
          </Button>
        </div>
      </div>

      {custom && (
        <div className="space-y-2">
          <Label htmlFor="vendor-status-url">{t("vendorStatus.url")}</Label>
          <Input
            id="vendor-status-url"
            type="url"
            placeholder="https://status.acme.com"
            value={state.url}
            onChange={(e) => onChange({ ...state, url: e.target.value, provider: "", components: [] })}
            className={cn(getFieldError(errors, "url") && "border-destructive")}
            data-testid="check-vendor-status-url-input"
          />
          <p className="text-xs text-muted-foreground">{t("vendorStatus.urlHelp")}</p>
        </div>
      )}
      {getFieldError(errors, "url") && (
        <p className="text-xs text-destructive" data-testid="check-vendor-status-url-error">
          {getFieldError(errors, "url")}
        </p>
      )}

      {state.url.trim() !== "" && (
        <div className="space-y-2" data-testid="check-vendor-status-components">
          <Label>{t("vendorStatus.components")}</Label>
          <p className="text-xs text-muted-foreground">{t("vendorStatus.componentsHelp")}</p>
          {components.isLoading && <p className="text-sm text-muted-foreground">{t("vendorStatus.loading")}</p>}
          {components.isError && (
            <p className="text-sm text-destructive" data-testid="check-vendor-status-components-error">
              {t("vendorStatus.componentsError")}
            </p>
          )}
          <ul className="space-y-1">
            {(components.data ?? []).map((component) => (
              <li key={component.id} className="flex items-center gap-2">
                <Checkbox
                  id={`vendor-status-component-${component.id}`}
                  checked={state.components.includes(component.id)}
                  onCheckedChange={(on) => toggle(component.id, on === true)}
                  data-testid={`check-vendor-status-component-${component.id}`}
                />
                <Label htmlFor={`vendor-status-component-${component.id}`} className="font-normal">
                  {component.group ? `${component.group} / ${component.name}` : component.name}
                </Label>
              </li>
            ))}
          </ul>
        </div>
      )}
    </>
  );
}

export const vendorStatusModule: CheckTypeModule<VendorStatusState> = {
  types: ["vendor-status"],
  ownedKeys: ["url", "components", "provider"],
  fromConfig: (config: CheckConfig) => ({
    url: getConfigField(config, "url"),
    provider: getConfigField(config, "provider"),
    components: asList(config.components),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    const url = state.url.trim();
    if (url) cfg.url = url;
    if (state.provider) cfg.provider = state.provider;
    if (state.components.length > 0) cfg.components = state.components;
    const errors = [];
    if (!url) errors.push({ name: "url", message: validationMessage("vendorStatusUrl") });
    else if (!/^https:\/\/[^/]+/i.test(url)) {
      errors.push({ name: "url", message: validationMessage("vendorStatusHttps") });
    }
    return { config: cfg, errors };
  },
  Fields,
};
