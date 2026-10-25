/**
 * ESLint rule: require-testid-on-interactive
 *
 * E2E specs and showcase videos select on `data-testid`. An interactive
 * element without one forces a text locator, which breaks on a copy change or
 * a new locale.
 *
 * Interactive: <Button>, <Input>, <Textarea>, <SelectTrigger>, <Checkbox>,
 * <Switch>, <a>, <Link>, <form>, <DialogContent>, <AlertDialogContent> and the
 * native <button>, <input>, <select>, <textarea>.
 *
 * A spread prop ({...props}) counts as satisfied: the shared primitives in
 * src/components/ui forward the id from their callers instead of hard-coding
 * one. Elements that are not interactive (a div with onClick) are out of
 * scope. Only files under src/ are checked.
 */
const INTERACTIVE = new Set([
  "Button", "Input", "Textarea", "SelectTrigger", "Checkbox", "Switch", "Link",
  "DialogContent", "AlertDialogContent",
  "a", "form", "button", "input", "select", "textarea",
]);

function inScope(filename) {
  return /(^|[\\/])src[\\/]/.test(filename);
}

/** @type {import("eslint").Rule.RuleModule} */
export default {
  meta: {
    type: "problem",
    docs: { description: "Require data-testid on interactive JSX elements" },
    schema: [],
    messages: {
      missing: "<{{name}}> needs a data-testid (kebab-case, descriptive), or a spread prop that forwards one.",
    },
  },
  create(context) {
    if (!inScope(context.filename)) return {};
    return {
      JSXOpeningElement(node) {
        if (node.name.type !== "JSXIdentifier") return;
        const name = node.name.name;
        if (!INTERACTIVE.has(name)) return;
        const ok = node.attributes.some(
          (a) =>
            a.type === "JSXSpreadAttribute" ||
            (a.type === "JSXAttribute" && a.name.type === "JSXIdentifier" && a.name.name === "data-testid"),
        );
        if (!ok) context.report({ node, messageId: "missing", data: { name } });
      },
    };
  },
};
