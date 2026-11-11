import * as React from "react";
import { cn } from "@/lib/utils";
import { NO_CAPTURE_CLASS } from "@/lib/no-capture";

type SecretTextProps = (
  | ({ as?: "code" } & React.ComponentPropsWithoutRef<"code">)
  | ({ as: "pre" } & React.ComponentPropsWithoutRef<"pre">)
  | ({ as: "div" } & React.ComponentPropsWithoutRef<"div">)
  | ({ as: "span" } & React.ComponentPropsWithoutRef<"span">)
);

/**
 * Every secret shown to the user as text goes through this: a shown-once
 * token, a generated key, a signing secret, recovery codes, a command line
 * that embeds one. It renders a `<code>` (or `<pre>`, `<div>`, `<span>`) that
 * always carries {@link NO_CAPTURE_CLASS} and `data-secret-text`, so neither
 * session replay nor autocapture can record its content.
 */
export function SecretText({ as = "code", className, ...props }: SecretTextProps) {
  const Element = as as "code";
  return (
    <Element
      {...(props as React.ComponentPropsWithoutRef<"code">)}
      className={cn(NO_CAPTURE_CLASS, className)}
      data-secret-text=""
    />
  );
}
