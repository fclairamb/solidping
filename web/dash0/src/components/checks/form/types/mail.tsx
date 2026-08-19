import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { getFieldError } from "@/hooks/use-check-validation";
import { useChecks } from "@/api/hooks";
import { checkLabel } from "@/components/checks/tunnel";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";
import { useCheckFormFields } from "./context";

const hostRequired = (host: string): FieldErrors =>
  host ? [] : [{ name: "host", message: "Host is required" }];

// smtpSendModeErrors validates the send-mode fields on top of the plain
// host requirement — mail_from and delivery_check_uid are only required when
// send mode itself is on (spec 2026-08-19-04).
function smtpSendModeErrors(state: SmtpState): FieldErrors {
  const errors = hostRequired(state.host);
  if (!state.sendEmail) return errors;
  if (!state.mailFrom) {
    errors.push({ name: "mail_from", message: "Mail From is required when send mode is enabled" });
  }
  if (!state.deliveryCheckUid) {
    errors.push({
      name: "delivery_check_uid",
      message: "Select a delivery (email) check when send mode is enabled",
    });
  }
  return errors;
}

// ── SMTP ──
export interface SmtpState {
  host: string;
  port: string;
  startTLS: boolean;
  tlsVerify: boolean;
  checkAuth: boolean;
  ehloDomain: string;
  expectGreeting: string;
  username: string;
  password: string;
  // Send mode (spec 2026-08-19-04): submits a real, system-generated probe
  // email through the monitored server, addressed to a paired email check's
  // tokenized address.
  sendEmail: boolean;
  mailFrom: string;
  deliveryCheckUid: string;
}

export const smtpModule: CheckTypeModule<SmtpState> = {
  types: ["smtp"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    startTLS: getConfigField(config, "starttls") === "true",
    tlsVerify: getConfigField(config, "tls_verify") === "true",
    checkAuth: getConfigField(config, "check_auth") === "true",
    ehloDomain: getConfigField(config, "ehlo_domain"),
    expectGreeting: getConfigField(config, "expect_greeting"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    sendEmail: getConfigField(config, "send_email") === "true",
    mailFrom: getConfigField(config, "mail_from"),
    deliveryCheckUid: getConfigField(config, "delivery_check_uid"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.startTLS) cfg.starttls = true;
    if (state.tlsVerify) cfg.tls_verify = true;
    if (state.ehloDomain) cfg.ehlo_domain = state.ehloDomain;
    if (state.expectGreeting) cfg.expect_greeting = state.expectGreeting;
    if (state.checkAuth) cfg.check_auth = true;
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.sendEmail) {
      cfg.send_email = true;
      if (state.mailFrom) cfg.mail_from = state.mailFrom;
      if (state.deliveryCheckUid) cfg.delivery_check_uid = state.deliveryCheckUid;
    }
    return { config: cfg, errors: smtpSendModeErrors(state) };
  },
  Fields: SmtpFields,
};

function SmtpFields({ state, onChange, errors }: CheckTypeFieldsProps<SmtpState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="mail.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder="25"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
            data-testid="check-port-input"
          />
        </div>
      </div>
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.startTLS}
            onCheckedChange={(v) => onChange({ ...state, startTLS: v === true })}
            data-testid="check-starttls-checkbox"
          />
          <span className="text-sm">Use STARTTLS</span>
        </label>
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.tlsVerify}
            onCheckedChange={(v) => onChange({ ...state, tlsVerify: v === true })}
            data-testid="check-tls-verify-checkbox"
          />
          <span className="text-sm">Verify TLS certificate</span>
        </label>
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.checkAuth}
            onCheckedChange={(v) => onChange({ ...state, checkAuth: v === true })}
            data-testid="check-auth-checkbox"
          />
          <span className="text-sm">Check AUTH support</span>
        </label>
      </div>
      <div className="space-y-2">
        <Label htmlFor="ehloDomain">EHLO Domain (optional)</Label>
        <Input
          id="ehloDomain"
          type="text"
          placeholder="example.com"
          value={state.ehloDomain}
          onChange={(e) => onChange({ ...state, ehloDomain: e.target.value })}
          data-testid="check-ehlo-domain-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="expectGreeting">Expected Greeting (optional)</Label>
        <Input
          id="expectGreeting"
          type="text"
          placeholder="220"
          value={state.expectGreeting}
          onChange={(e) => onChange({ ...state, expectGreeting: e.target.value })}
          data-testid="check-expect-greeting-input"
        />
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="username">Username (optional, AUTH PLAIN)</Label>
          <Input
            id="username"
            type="text"
            placeholder="user@example.com"
            value={state.username}
            onChange={(e) => onChange({ ...state, username: e.target.value })}
            data-testid="check-username-input"
          />
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="password">Password (optional)</Label>
          <Input
            id="password"
            type="password"
            value={state.password}
            onChange={(e) => onChange({ ...state, password: e.target.value })}
            data-testid="check-password-input"
          />
        </div>
      </div>
      <SmtpSendModeFields state={state} onChange={onChange} errors={errors} />
    </>
  );
}

// SmtpSendModeFields renders the send-mode toggle and, when enabled, the
// mail_from input and the same-org email-check picker. Per the spec's
// resolved open question: no "create a delivery check for me" affordance
// anywhere here — the picker only, plain text when the org has none yet.
function SmtpSendModeFields({ state, onChange, errors }: CheckTypeFieldsProps<SmtpState>) {
  const { org } = useCheckFormFields();
  const { data: emailChecks } = useChecks(org, { type: "email", limit: 100 });
  const candidates = emailChecks ?? [];

  return (
    <div className="space-y-3 rounded-md border p-3" data-testid="check-smtp-send-mode-section">
      <label className="flex items-center gap-2">
        <Switch
          checked={state.sendEmail}
          onCheckedChange={(sendEmail) => onChange({ ...state, sendEmail })}
          data-testid="check-smtp-send-email-switch"
        />
        <span className="text-sm font-medium">Send a probe email</span>
      </label>
      <p className="text-xs text-muted-foreground">
        Submits a system-generated email through this server on every check, addressed to a
        paired email check&apos;s tokenized address, to verify the full delivery path (not just
        the handshake). The email check&apos;s period is the delivery deadline — size it to
        this check&apos;s interval plus the worst acceptable delivery time (greylisting can add
        minutes).
      </p>
      {state.sendEmail && (
        <>
          <div className="space-y-2">
            <Label htmlFor="smtp-mail-from">Mail From</Label>
            <Input
              id="smtp-mail-from"
              type="text"
              placeholder="prober@example.com"
              value={state.mailFrom}
              onChange={(e) => onChange({ ...state, mailFrom: e.target.value })}
              className={cn(getFieldError(errors, "mail_from") && "border-destructive")}
              data-testid="check-smtp-mail-from-input"
            />
            {getFieldError(errors, "mail_from") && (
              <p className="text-xs text-destructive">{getFieldError(errors, "mail_from")}</p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="smtp-delivery-check">Delivery (email) check</Label>
            {candidates.length === 0 ? (
              <Alert>
                <AlertDescription>
                  No email checks in this organization yet. Create one first, then come back
                  to pair it here.
                </AlertDescription>
              </Alert>
            ) : (
              <Select
                value={state.deliveryCheckUid}
                onValueChange={(deliveryCheckUid) => onChange({ ...state, deliveryCheckUid })}
              >
                <SelectTrigger id="smtp-delivery-check" data-testid="check-smtp-delivery-select">
                  <SelectValue placeholder="Select an email check" />
                </SelectTrigger>
                <SelectContent>
                  {candidates.map((check) => (
                    <SelectItem key={check.uid} value={check.uid}>
                      {checkLabel(check)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            {getFieldError(errors, "delivery_check_uid") && (
              <p className="text-xs text-destructive">
                {getFieldError(errors, "delivery_check_uid")}
              </p>
            )}
          </div>
        </>
      )}
    </div>
  );
}

// ── POP3 / IMAP ──
export interface MailboxState {
  host: string;
  port: string;
  tls: boolean;
  startTLS: boolean;
  username: string;
  password: string;
}

export const mailboxModule: CheckTypeModule<MailboxState> = {
  types: ["pop3", "imap"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    tls: getConfigField(config, "tls") === "true",
    startTLS: getConfigField(config, "starttls") === "true",
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.tls) cfg.tls = true;
    if (state.startTLS) cfg.starttls = true;
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: MailboxFields,
};

function MailboxFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<MailboxState>) {
  // D2: selecting the implicit-TLS well-known port (993 IMAP / 995 POP3)
  // auto-checks the TLS toggle; unchecking TLS restores the plaintext port as
  // the placeholder. Purely client-side guidance — the server derives
  // independently regardless of what the client sends.
  const { type } = useCheckFormFields();
  const implicitTLSPort = type === "pop3" ? "995" : "993";
  const plaintextPort = type === "pop3" ? "110" : "143";
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="mail.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className={cn(
              "flex-1",
              getFieldError(errors, "host") && "border-destructive",
            )}
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder={state.tls ? implicitTLSPort : plaintextPort}
            value={state.port}
            onChange={(e) => {
              const value = e.target.value;
              onChange({
                ...state,
                port: value,
                tls:
                  value === implicitTLSPort && !state.startTLS ? true : state.tls,
              });
            }}
            className={cn(
              "w-24",
              getFieldError(errors, "port") && "border-destructive",
            )}
            data-testid="check-port-input"
          />
        </div>
        {getFieldError(errors, "host") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "host")}
          </p>
        )}
        {getFieldError(errors, "port") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "port")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.tls}
            onCheckedChange={(v) => onChange({ ...state, tls: v === true })}
            data-testid="check-tls-checkbox"
          />
          <span className="text-sm">Use implicit TLS</span>
        </label>
        {(state.tls || state.port === implicitTLSPort) && (
          <p className="text-xs text-muted-foreground">
            Port {implicitTLSPort} uses implicit TLS.
          </p>
        )}
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.startTLS}
            onCheckedChange={(v) => onChange({ ...state, startTLS: v === true })}
            data-testid="check-starttls-checkbox"
          />
          <span className="text-sm">Use STARTTLS</span>
        </label>
      </div>
      <div className="space-y-2">
        <Label htmlFor="username">Username (optional)</Label>
        <Input
          id="username"
          type="text"
          placeholder="user"
          value={state.username}
          onChange={(e) => onChange({ ...state, username: e.target.value })}
          data-testid="check-username-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="password">Password (optional)</Label>
        <Input
          id="password"
          type="password"
          value={state.password}
          onChange={(e) => onChange({ ...state, password: e.target.value })}
          data-testid="check-password-input"
        />
      </div>
    </>
  );
}
