import { useEffect, useRef } from "react";

export interface HotkeyOptions {
  /** Fire even when the event target is an input, textarea, select or contenteditable. */
  allowInEditable?: boolean;
  /** Require Ctrl or Cmd (Meta). Without it, events with Ctrl/Cmd/Alt held are ignored. */
  mod?: boolean;
  /** Call preventDefault() when the handler runs. */
  preventDefault?: boolean;
  /** Disable the shortcut without unregistering the hook. */
  enabled?: boolean;
}

export function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName;
  return (
    tag === "INPUT" ||
    tag === "TEXTAREA" ||
    tag === "SELECT" ||
    target.isContentEditable ||
    target.getAttribute("contenteditable") === "true" ||
    target.getAttribute("contenteditable") === ""
  );
}

/**
 * Page-level keyboard shortcut. Registers on `document` keydown. Ignores
 * events already handled (`defaultPrevented`, e.g. Radix layers closing on
 * Esc), events typed into editable elements (unless `allowInEditable`), and
 * events with Ctrl/Cmd/Alt held (unless `mod`).
 */
export function useHotkey(
  key: string,
  handler: (e: KeyboardEvent) => void,
  options: HotkeyOptions = {},
): void {
  const handlerRef = useRef(handler);
  useEffect(() => {
    handlerRef.current = handler;
  });
  const { allowInEditable = false, mod = false, preventDefault = false, enabled = true } = options;

  useEffect(() => {
    if (!enabled) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing) return;
      if (e.key !== key) return;
      if (mod) {
        if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
      } else if (e.metaKey || e.ctrlKey || e.altKey) {
        return;
      }
      if (!allowInEditable && isEditableTarget(e.target)) return;
      if (preventDefault) e.preventDefault();
      handlerRef.current(e);
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [key, allowInEditable, mod, preventDefault, enabled]);
}

/**
 * Cmd/Ctrl+Enter clicks the page's primary submit button, the one marked
 * `data-submit-shortcut`, so it takes the Save-button path (validation
 * included) and does nothing while that button is disabled (save pending).
 */
export function useSubmitShortcut(): void {
  useHotkey(
    "Enter",
    () => {
      const btn = document.querySelector<HTMLButtonElement>("[data-submit-shortcut]");
      if (btn && !btn.disabled) btn.click();
    },
    { mod: true, allowInEditable: true, preventDefault: true },
  );
}
