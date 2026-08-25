import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";

// ── WebSocket ──
export interface WebsocketState {
  url: string;
  send: string;
  expect: string;
}

export const websocketModule: CheckTypeModule<WebsocketState> = {
  types: ["websocket"],
  fromConfig: (config) => ({
    url: getConfigField(config, "url"),
    send: getConfigField(config, "send"),
    expect: getConfigField(config, "expect"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.url) cfg.url = state.url;
    if (state.send) cfg.send = state.send;
    if (state.expect) cfg.expect = state.expect;
    const errors: FieldErrors = state.url
      ? []
      : [{ name: "url", message: "URL is required" }];
    return { config: cfg, errors };
  },
  Fields: WebsocketFields,
};

function WebsocketFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<WebsocketState>) {
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="url">URL</Label>
        <Input
          id="url"
          type="url"
          placeholder="wss://example.com/ws"
          value={state.url}
          onChange={(e) => onChange({ ...state, url: e.target.value })}
          className={cn(getFieldError(errors, "url") && "border-destructive")}
          data-testid="check-url-input"
        />
        {getFieldError(errors, "url") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "url")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="wsSend">Send (optional)</Label>
        <Input
          id="wsSend"
          type="text"
          placeholder="hello"
          value={state.send}
          onChange={(e) => onChange({ ...state, send: e.target.value })}
          data-testid="check-ws-send-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="wsExpect">Expected pattern (regex, optional)</Label>
        <Input
          id="wsExpect"
          type="text"
          placeholder="hello"
          value={state.expect}
          onChange={(e) => onChange({ ...state, expect: e.target.value })}
          data-testid="check-ws-expect-input"
        />
      </div>
    </>
  );
}

// ── Browser ──
export interface BrowserState {
  url: string;
  waitSelector: string;
  keyword: string;
  screenshot: boolean;
}

export const browserModule: CheckTypeModule<BrowserState> = {
  types: ["browser"],
  fromConfig: (config) => ({
    url: getConfigField(config, "url"),
    waitSelector: getConfigField(config, "waitSelector"),
    keyword: getConfigField(config, "keyword"),
    screenshot: getConfigField(config, "screenshot") === "true",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.url) cfg.url = state.url;
    if (state.waitSelector) cfg.waitSelector = state.waitSelector;
    if (state.keyword) cfg.keyword = state.keyword;
    // Only sent when on: the backend default is off, and an explicit `false`
    // would add noise to every browser check's stored config.
    if (state.screenshot) cfg.screenshot = true;
    const errors: FieldErrors = state.url
      ? []
      : [{ name: "url", message: "URL is required" }];
    return { config: cfg, errors };
  },
  Fields: BrowserFields,
};

function BrowserFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<BrowserState>) {
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="url">URL</Label>
        <Input
          id="url"
          type="url"
          placeholder="https://example.com"
          value={state.url}
          onChange={(e) => onChange({ ...state, url: e.target.value })}
          className={cn(getFieldError(errors, "url") && "border-destructive")}
          data-testid="check-url-input"
        />
        {getFieldError(errors, "url") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "url")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="waitSelector">Wait Selector (optional)</Label>
        <Input
          id="waitSelector"
          type="text"
          placeholder="#main-content"
          value={state.waitSelector}
          onChange={(e) => onChange({ ...state, waitSelector: e.target.value })}
          data-testid="check-wait-selector-input"
        />
        <p className="text-xs text-muted-foreground">
          CSS selector to wait for before checking. Leave empty to wait for
          body.
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="keyword">Keyword (optional)</Label>
        <Input
          id="keyword"
          type="text"
          placeholder="Welcome"
          value={state.keyword}
          onChange={(e) => onChange({ ...state, keyword: e.target.value })}
          data-testid="check-keyword-input"
        />
        <p className="text-xs text-muted-foreground">
          Text to search for in the rendered page content.
        </p>
      </div>
      <div className="space-y-2">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.screenshot}
            onCheckedChange={(v) =>
              onChange({ ...state, screenshot: v === true })
            }
            data-testid="check-browser-screenshot-checkbox"
          />
          <span className="text-sm">Capture a screenshot on failure</span>
        </label>
        <p className="text-xs text-muted-foreground">
          Keeps a PNG of the page on the incident this check opens or reopens.
          Taken just after the failure is detected, from the probing region —
          evidence, not the failing frame itself. Off by default.
        </p>
      </div>
    </>
  );
}
