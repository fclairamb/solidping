// @vitest-environment jsdom
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useHotkey } from "./use-hotkey";

function Probe(props: {
  k: string;
  fn: () => void;
  opts?: Parameters<typeof useHotkey>[2];
}) {
  useHotkey(props.k, props.fn, props.opts);
  return <input data-testid="field" />;
}

function press(target: Element | Document, init: KeyboardEventInit) {
  const ev = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(ev);
  return ev;
}

describe("useHotkey", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("fires on the matching key", () => {
    const fn = vi.fn();
    render(<Probe k="n" fn={fn} />);
    press(document.body, { key: "n" });
    press(document.body, { key: "x" });
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("ignores editable targets unless opted in", () => {
    const fn = vi.fn();
    const { getByTestId, rerender } = render(<Probe k="n" fn={fn} />);
    press(getByTestId("field"), { key: "n" });
    expect(fn).not.toHaveBeenCalled();
    rerender(<Probe k="n" fn={fn} opts={{ allowInEditable: true }} />);
    press(getByTestId("field"), { key: "n" });
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("ignores modifiers unless declared", () => {
    const fn = vi.fn();
    const { rerender } = render(<Probe k="Enter" fn={fn} />);
    press(document.body, { key: "Enter", metaKey: true });
    press(document.body, { key: "Enter", ctrlKey: true });
    press(document.body, { key: "Enter", altKey: true });
    expect(fn).not.toHaveBeenCalled();
    rerender(<Probe k="Enter" fn={fn} opts={{ mod: true }} />);
    press(document.body, { key: "Enter" });
    expect(fn).not.toHaveBeenCalled();
    press(document.body, { key: "Enter", metaKey: true });
    press(document.body, { key: "Enter", ctrlKey: true });
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("ignores events already defaultPrevented", () => {
    const fn = vi.fn();
    render(<Probe k="Escape" fn={fn} />);
    const first = (e: Event) => e.preventDefault();
    document.addEventListener("keydown", first, true);
    press(document.body, { key: "Escape" });
    document.removeEventListener("keydown", first, true);
    expect(fn).not.toHaveBeenCalled();
    press(document.body, { key: "Escape" });
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("cleans up on unmount", () => {
    const fn = vi.fn();
    const { unmount } = render(<Probe k="n" fn={fn} />);
    unmount();
    press(document.body, { key: "n" });
    expect(fn).not.toHaveBeenCalled();
  });
});
