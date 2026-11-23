import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField, validationMessage } from "./common";
import {
  applyPayload,
  PAYLOAD_KEYS,
  PayloadFields,
  payloadFromConfig,
} from "./network";
import type { PayloadState } from "./network";

// ── Unix socket (issue #1000) ──

/**
 * `connect` only opens the socket, `raw` runs the tcp send/expect exchange,
 * `http` sends `GET http_path` over the socket. The mode is form state only:
 * the backend infers it from which keys are set, so switching it drops the
 * other mode's keys (all of them are in `ownedKeys`).
 */
export type UnixSocketMode = "connect" | "raw" | "http";
const UNIX_SOCKET_MODES: readonly UnixSocketMode[] = ["connect", "raw", "http"];

export interface UnixSocketState extends PayloadState {
  path: string;
  mode: UnixSocketMode;
  httpPath: string;
  expectedStatus: string;
  bodyExpect: string;
}

export const unixSocketModule: CheckTypeModule<UnixSocketState> = {
  types: ["unix_socket"],
  ownedKeys: [
    "path",
    ...PAYLOAD_KEYS,
    "http_path",
    "expected_status",
    "body_expect",
  ],
  fromConfig: (config) => {
    const payload = payloadFromConfig(config);
    const httpPath = getConfigField(config, "http_path");
    let mode: UnixSocketMode = "connect";
    if (httpPath) mode = "http";
    else if (payload.sendData || payload.expectValue) mode = "raw";
    return {
      ...payload,
      path: getConfigField(config, "path"),
      mode,
      httpPath,
      expectedStatus: getConfigField(config, "expected_status"),
      bodyExpect: getConfigField(config, "body_expect"),
    };
  },
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.path) cfg.path = state.path;
    if (state.mode === "raw") applyPayload(cfg, state);
    if (state.mode === "http") {
      cfg.http_path = state.httpPath || "/";
      if (state.expectedStatus) {
        cfg.expected_status = parseInt(state.expectedStatus, 10);
      }
      if (state.bodyExpect) cfg.body_expect = state.bodyExpect;
    }
    const errors: FieldErrors = [];
    if (!state.path) {
      errors.push({
        name: "path",
        message: validationMessage("socketPathRequired"),
      });
    } else if (!state.path.startsWith("/")) {
      errors.push({
        name: "path",
        message: validationMessage("socketPathAbsolute"),
      });
    }
    return { config: cfg, errors };
  },
  Fields: UnixSocketFields,
};

function UnixSocketFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<UnixSocketState>) {
  const { t } = useTranslation("checks");
  const pathError = getFieldError(errors, "path");
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="socketPath">{t("unixSocket.path")}</Label>
        <Input
          id="socketPath"
          type="text"
          placeholder="/run/app/app.sock"
          value={state.path}
          onChange={(e) => onChange({ ...state, path: e.target.value })}
          className={cn("font-mono", pathError && "border-destructive")}
          data-testid="check-socket-path-input"
        />
        {pathError && <p className="text-xs text-destructive">{pathError}</p>}
        <p className="text-xs text-muted-foreground">
          {t("unixSocket.pathHelp")}
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="socketMode">{t("unixSocket.mode")}</Label>
        <Select
          value={state.mode}
          onValueChange={(mode) =>
            onChange({ ...state, mode: mode as UnixSocketMode })
          }
        >
          <SelectTrigger id="socketMode" data-testid="check-socket-mode-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {UNIX_SOCKET_MODES.map((mode) => (
              <SelectItem
                key={mode}
                value={mode}
                data-testid={`check-socket-mode-${mode}`}
              >
                {t(`unixSocket.mode_${mode}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {state.mode === "raw" && (
        <PayloadFields
          state={state}
          onChange={onChange}
          sendPlaceholder={String.raw`PING\r\n`}
        />
      )}
      {state.mode === "http" && (
        <>
          <div className="space-y-2">
            <Label htmlFor="socketHttpPath">{t("unixSocket.httpPath")}</Label>
            <Input
              id="socketHttpPath"
              type="text"
              placeholder="/healthcheck"
              value={state.httpPath}
              onChange={(e) => onChange({ ...state, httpPath: e.target.value })}
              className="font-mono"
              data-testid="check-socket-http-path-input"
            />
          </div>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="socketExpectedStatus">
                {t("unixSocket.expectedStatusOptional")}
              </Label>
              <Input
                id="socketExpectedStatus"
                type="number"
                min="100"
                max="599"
                placeholder="200"
                value={state.expectedStatus}
                onChange={(e) =>
                  onChange({ ...state, expectedStatus: e.target.value })
                }
                data-testid="check-socket-expected-status-input"
              />
              <p className="text-xs text-muted-foreground">
                {t("unixSocket.expectedStatusHelp")}
              </p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="socketBodyExpect">
                {t("unixSocket.bodyExpectOptional")}
              </Label>
              <Input
                id="socketBodyExpect"
                type="text"
                placeholder='"status":"ok"'
                value={state.bodyExpect}
                onChange={(e) =>
                  onChange({ ...state, bodyExpect: e.target.value })
                }
                data-testid="check-socket-body-expect-input"
              />
            </div>
          </div>
        </>
      )}
    </>
  );
}
