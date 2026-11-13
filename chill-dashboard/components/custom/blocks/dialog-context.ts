"use client";

import { createContext, useContext } from "react";

interface DialogContextType {
  open: boolean;
  setOpen: (open: boolean) => void;
  /** Id of the DialogTitle, used as the dialog's accessible name. */
  titleId: string;
}

export const DialogContext = createContext<DialogContextType | undefined>(undefined);

export function useDialog() {
  const context = useContext(DialogContext);
  if (!context)
    throw new Error("Dialog components must be used within a <Dialog> root.");
  return context;
}

/**
 * The element to refocus when a modal closes. The browser returns focus to whatever was focused at
 * showModal(); a menu item that opened the modal is gone by then, so remember its menu button instead
 * (a menu is labelled by its button).
 */
export function dialogOpener(): HTMLElement | null {
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const button = active?.closest("[role=menu]")?.getAttribute("aria-labelledby");
  return (button && document.getElementById(button)) || active;
}

/** Call from the dialog's `close` event: refocuses the opener only where the browser's own return failed. */
export function refocusOpener(opener: HTMLElement | null) {
  const active = document.activeElement;
  if (opener?.isConnected && (!active || active === document.body)) opener.focus();
}
