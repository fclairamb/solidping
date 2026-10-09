import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Chip } from "@/components/ui/chip";
import { COMMON_PORTS } from "./types/ports";

export const isValidPort = (value: string): boolean => {
  if (!/^\d+$/.test(value)) return false;
  const n = parseInt(value, 10);
  return n >= 1 && n <= 65535;
};

/** Text-mode numeric port input: no browser spinner, digits only. */
export function PortInput({
  value,
  onChange,
  invalid,
  placeholder,
  className,
}: {
  value: string;
  onChange: (next: string) => void;
  invalid?: boolean;
  placeholder?: string;
  className?: string;
}) {
  const outOfRange = value !== "" && !isValidPort(value);
  return (
    <Input
      id="port"
      type="text"
      inputMode="numeric"
      pattern="[0-9]*"
      maxLength={5}
      placeholder={placeholder}
      value={value}
      onChange={(e) => onChange(e.target.value.replace(/\D/g, ""))}
      aria-invalid={invalid || outOfRange || undefined}
      className={cn(
        "w-24",
        (invalid || outOfRange) && "border-destructive",
        className,
      )}
      data-testid="check-port-input"
    />
  );
}

/** Client-side range message, shown only once something was typed. */
export function PortRangeError({ value }: { value: string }) {
  const { t } = useTranslation("checks");
  if (value === "" || isValidPort(value)) return null;
  return (
    <p className="text-xs text-destructive" data-testid="check-port-range-error">
      {t("form.portOutOfRange")}
    </p>
  );
}

/** Common-port chips for a type; renders nothing for types without any. */
export function PortChips({
  type,
  value,
  onSelect,
}: {
  type: string;
  value: string;
  onSelect: (port: string) => void;
}) {
  const ports = COMMON_PORTS[type];
  if (!ports) return null;
  return (
    <div className="flex flex-wrap gap-1.5" data-testid="check-port-chips">
      {ports.map((p) => (
        <Chip
          key={p.port}
          selected={value === p.port}
          onClick={() => onSelect(p.port)}
          data-testid={`check-port-chip-${p.port}`}
        >
          {p.port} {p.label}
        </Chip>
      ))}
    </div>
  );
}
