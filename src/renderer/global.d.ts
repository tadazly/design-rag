import type { DragDesktopApi } from "../shared/contracts.js";

declare global {
  interface Window {
    drag?: DragDesktopApi;
  }
}

export {};
