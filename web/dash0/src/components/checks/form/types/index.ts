// Per-check-type module registry (spec §3).
//
// One module per check type behind a common interface. `CheckForm` holds a
// single `configState` object for the active type, re-seeds it via
// `module.fromConfig` on type change, calls `module.toConfig` ONCE for both the
// live preview and the submitted payload, and renders `module.Fields`. Adding a
// check type is now a one-file change plus a registry entry — no more four
// parallel `switch (type)` blocks kept in sync by hand.
import type { FC } from "react";
import type {
  CheckConfig,
  CheckType,
  CheckTypeFieldsProps,
  FieldErrors,
} from "./common";

export interface CheckTypeModule<S = unknown> {
  types: CheckType[];
  fromConfig(config: CheckConfig): S;
  toConfig(state: S): { config: CheckConfig; errors: FieldErrors };
  Fields: FC<CheckTypeFieldsProps<S>>;
}

import {
  httpModule,
  HttpAuthFields,
  httpAuthSummary,
  HttpOptionsFields,
  httpOptionsSummary,
} from "./http";
import type { HttpState } from "./http";
import { websocketModule, browserModule } from "./web";
import {
  tcpModule,
  sshModule,
  sftpModule,
  ftpModule,
  icmpModule,
} from "./network";
import { smtpModule, mailboxModule } from "./mail";
import {
  dnsModule,
  domainModule,
  dnsblModule,
  DomainAdvancedFields,
  domainAdvancedSummary,
} from "./dns";
import type { DomainState } from "./dns";
import {
  sqlDatabaseModule,
  redisModule,
  mongodbModule,
  rabbitmqModule,
} from "./database";
import { clickhouseModule } from "./clickhouse";
import { grpcModule, kafkaModule, mqttModule } from "./messaging";
import { a2sModule, minecraftModule } from "./game";
import {
  snmpModule,
  dockerModule,
  freeboxLineModule,
  prometheusModule,
} from "./infra";
import {
  sslModule,
  ntpModule,
  rdpModule,
  sipModule,
  jsModule,
  sleepModule,
  heartbeatModule,
  emailModule,
} from "./misc";

// Widen a concrete `CheckTypeModule<S>` to the registry's `unknown` state type.
// The form only ever pairs a module with the `configState` it produced, so the
// erasure is sound.
function entry<S>(m: CheckTypeModule<S>): CheckTypeModule {
  return m as unknown as CheckTypeModule;
}

const modules: CheckTypeModule[] = [
  entry(httpModule),
  entry(websocketModule),
  entry(browserModule),
  entry(tcpModule),
  entry(sshModule),
  entry(sftpModule),
  entry(ftpModule),
  entry(icmpModule),
  entry(smtpModule),
  entry(mailboxModule),
  entry(dnsModule),
  entry(domainModule),
  entry(dnsblModule),
  entry(sqlDatabaseModule),
  entry(redisModule),
  entry(mongodbModule),
  entry(rabbitmqModule),
  entry(clickhouseModule),
  entry(grpcModule),
  entry(kafkaModule),
  entry(mqttModule),
  entry(a2sModule),
  entry(minecraftModule),
  entry(snmpModule),
  entry(prometheusModule),
  entry(dockerModule),
  entry(freeboxLineModule),
  entry(sslModule),
  entry(ntpModule),
  entry(rdpModule),
  entry(sipModule),
  entry(jsModule),
  entry(sleepModule),
  entry(heartbeatModule),
  entry(emailModule),
];

export const checkTypeRegistry: Record<CheckType, CheckTypeModule> = (() => {
  const reg = {} as Record<CheckType, CheckTypeModule>;
  for (const m of modules) {
    for (const t of m.types) {
      reg[t] = m;
    }
  }
  return reg;
})();

// "Authentication & secrets" (spec §1) is a collapsible section separate from
// the always-visible protocol config. It is kept out of `CheckTypeModule` so
// that interface stays exactly as the spec defines it; only HTTP has one today.
export interface AuthSection {
  Fields: FC<CheckTypeFieldsProps>;
  // `configPrivateKeys` lets a summary account for secrets that are stored
  // encrypted and therefore absent from the form state.
  summary(
    state: unknown,
    configPrivateKeys?: string[],
  ): { text: string; customized: boolean };
}

export const authFieldsRegistry: Partial<Record<CheckType, AuthSection>> = {
  http: {
    Fields: HttpAuthFields as unknown as FC<CheckTypeFieldsProps>,
    summary: (state, configPrivateKeys) =>
      httpAuthSummary(state as HttpState, configPrivateKeys),
  },
};

// Per-type fields rendered inside the always-present "Advanced" section,
// alongside timeout/tunnel. Kept out of `CheckTypeModule` for the same
// reason as `AuthSection`; only HTTP has one today (verifySsl/followRedirects).
export interface AdvancedSection {
  Fields: FC<CheckTypeFieldsProps>;
  summary(state: unknown): { text: string; customized: boolean };
}

export const advancedFieldsRegistry: Partial<
  Record<CheckType, AdvancedSection>
> = {
  http: {
    Fields: HttpOptionsFields as unknown as FC<CheckTypeFieldsProps>,
    summary: (state) => httpOptionsSummary(state as HttpState),
  },
  domain: {
    Fields: DomainAdvancedFields as unknown as FC<CheckTypeFieldsProps>,
    summary: (state) => domainAdvancedSummary(state as DomainState),
  },
};
