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
