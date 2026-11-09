// Called directly from a user gesture. HTTP device pages do not expose the
// async Clipboard API, but still support the browser's normal copy command.
export function copyLocalText(text: string): Promise<void> {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
  const field = document.createElement("textarea");
  field.value = text;
  field.setAttribute("data-local-clipboard", "");
  field.style.cssText = "position:fixed;opacity:0;pointer-events:none";
  const previous = document.activeElement as HTMLElement | null;
  (previous?.parentElement ?? document.body).append(field);
  try {
    field.focus();
    field.select();
    if (!document.execCommand("copy")) throw new Error("Browser blocked clipboard write");
    return Promise.resolve();
  } catch (error) {
    return Promise.reject(error);
  } finally {
    field.remove();
    previous?.focus();
  }
}
