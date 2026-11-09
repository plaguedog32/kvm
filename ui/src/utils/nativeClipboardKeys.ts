// Keep physical modifiers separately from the remote HID state, which a guest
// clipboard transaction releases. Safari can omit keyup inside a Cmd chord.
export class NativeClipboardKeys {
  readonly swallowed = new Set<string>();
  readonly modifiers = new Set<string>();
  private restore = new Set<string>();
  private released = new Set<string>();
  down(event: { code: string; repeat: boolean }) {
    if (event.repeat && this.swallowed.has(event.code)) return true;
    if (!event.repeat) this.swallowed.delete(event.code);
    if (/^(Control|Meta|Alt|Shift)(Left|Right)$/.test(event.code)) this.modifiers.add(event.code);
    return false;
  }
  prepareRelease() {
    this.restore = new Set([...this.modifiers].filter(code => !code.startsWith("Meta")));
    this.released = new Set(this.restore);
  }
  // The backend releases its HID chord when the transaction ends. Re-arm only
  // the same physical modifier presses that survived from its start.
  finishRelease() {
    this.restore = new Set([...this.released].filter(code => this.modifiers.has(code)));
    this.released.clear();
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
    this.released.delete(event.code);
    if (event.key === "Meta") this.swallowed.clear();
    return this.swallowed.delete(event.code);
  }
  clear() {
    this.swallowed.clear();
    this.modifiers.clear();
    this.restore.clear();
    this.released.clear();
  }
}
