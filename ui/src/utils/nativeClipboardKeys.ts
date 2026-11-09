// Keep physical modifiers separately from the remote HID state, which a guest
// clipboard transaction releases. Safari can omit keyup inside a Cmd chord.
export class NativeClipboardKeys {
  readonly swallowed = new Set<string>();
  readonly modifiers = new Set<string>();
  private restore = new Set<string>();
  down(event: { code: string; repeat: boolean }) {
    if (event.repeat && this.swallowed.has(event.code)) return true;
    if (!event.repeat) this.swallowed.delete(event.code);
    if (/^(Control|Meta|Alt|Shift)(Left|Right)$/.test(event.code)) this.modifiers.add(event.code);
    return false;
  }
  prepareRelease() {
    this.restore = new Set([...this.modifiers].filter(code => !code.startsWith("Meta")));
  }
  claim(event: { code: string; repeat: boolean }) {
    if (event.repeat) return false;
    this.swallowed.add(event.code);
    return true;
  }
  // The backend releases its HID chord when the transaction ends. Re-arm only
  // the currently held non-Meta modifiers. AltGr restores stay blocked until
  // Alt is released, and synthetic Ctrl's keyup removes it from the set.
  finishRelease() {
    this.prepareRelease();
  }
  takeRestore(event: { code: string; altKey: boolean }) {
    if (event.altKey || /^(Control|Meta|Alt|Shift)(Left|Right)$/.test(event.code)) return [];
    const codes = [...this.restore].filter(code => this.modifiers.has(code) && code !== event.code);
    this.restore.clear();
    return codes;
  }
  up(event: { code: string; key: string }) {
    this.modifiers.delete(event.code);
    this.restore.delete(event.code);
    if (event.key === "Meta") this.swallowed.clear();
    return this.swallowed.delete(event.code);
  }
  clear() {
    this.swallowed.clear();
    this.modifiers.clear();
    this.restore.clear();
  }
}
