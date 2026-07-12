// Ambient form context for per-type `Fields` components.
//
// The `CheckTypeModule.Fields` signature is intentionally minimal
// (`state`/`onChange`/`errors`). A handful of protocol fields also need
// form-level context that is not part of the serialized config — the active
// type (so a module shared by several types can pick type-aware placeholders),
// the org, the loaded integrations (Freebox discovery / data-source pickers),
// the check's `configPrivateKeys` (encrypted-secret placeholders), and the name
// (the ICMP discovery picker back-fills an empty name). Exposing these through a
// context keeps the `Fields` prop contract exactly as the spec defines it.
import * as React from "react";
import type { Integration } from "@/api/hooks";
import type { CheckType } from "./common";

export interface CheckFormFieldsContextValue {
  type: CheckType;
  org: string;
  connections: Integration[] | undefined;
  configPrivateKeys: string[] | undefined;
  name: string;
  setName: (name: string) => void;
}

const CheckFormFieldsContext =
  React.createContext<CheckFormFieldsContextValue | null>(null);

export function CheckFormFieldsProvider({
  value,
  children,
}: {
  value: CheckFormFieldsContextValue;
  children: React.ReactNode;
}) {
  return (
    <CheckFormFieldsContext.Provider value={value}>
      {children}
    </CheckFormFieldsContext.Provider>
  );
}

export function useCheckFormFields(): CheckFormFieldsContextValue {
  const ctx = React.useContext(CheckFormFieldsContext);
  if (!ctx) {
    throw new Error(
      "useCheckFormFields must be used within a CheckFormFieldsProvider",
    );
  }
  return ctx;
}
