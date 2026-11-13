"use client";

// Avara UI Dialog, patched for CHILL: the panel is a native <dialog> opened
// with showModal(), so the browser provides focus containment, the inert
// page, Esc, top layer and focus return. The API (Dialog, DialogContent size,
// hideCloseButton, header/footer/title/description) is unchanged.
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { DialogContext, useDialog } from "./dialog-context";

export interface DialogProps {
  children: React.ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
}

export function Dialog({
  children,
  open: controlledOpen,
  defaultOpen = false,
  onOpenChange,
}: DialogProps) {
  const isControlled = controlledOpen !== undefined;
  const [uncontrolledOpen, setUncontrolledOpen] = useState(defaultOpen);
  const open = isControlled ? controlledOpen : uncontrolledOpen;
  const titleId = useId();
  const latest = useRef({ isControlled, onOpenChange });
  useEffect(() => {
    latest.current = { isControlled, onOpenChange };
  });
  const setOpen = useCallback((next: boolean) => {
    if (!latest.current.isControlled) setUncontrolledOpen(next);
    latest.current.onOpenChange?.(next);
  }, []);
  const value = useMemo(() => ({ open, setOpen, titleId }), [open, setOpen, titleId]);
  return <DialogContext.Provider value={value}>{children}</DialogContext.Provider>;
}

export function DialogTrigger({
  children,
  className,
  onClick,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  const { setOpen } = useDialog();
  return (
    <button
      type="button"
      className={className}
      onClick={(event) => {
        onClick?.(event);
        if (!event.defaultPrevented) setOpen(true);
      }}
      {...props}
    >
      {children}
    </button>
  );
}

export function DialogClose({
  children,
  className,
  onClick,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  const { setOpen } = useDialog();
  return (
    <button
      type="button"
      onClick={(event) => {
        onClick?.(event);
        setOpen(false);
      }}
      className={className}
      {...props}
    >
      {children}
    </button>
  );
}

const DIALOG_SIZES = {
  sm: "max-w-sm",
  md: "max-w-lg",
  lg: "max-w-2xl",
  xl: "max-w-4xl",
  full: "max-w-none h-dvh max-h-none rounded-none border-0",
} as const;
export type DialogSize = keyof typeof DIALOG_SIZES;

export interface DialogContentProps extends React.HTMLAttributes<HTMLDivElement> {
  hideCloseButton?: boolean;
  /** Panel width — `full` fills the viewport edge to edge. Defaults to `md`. */
  size?: DialogSize;
}

export function DialogContent({
  className,
  children,
  hideCloseButton = false,
  size = "md",
  ...props
}: DialogContentProps) {
  const { open, setOpen, titleId } = useDialog();
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    else if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      // Esc asks the owner; a controlled owner may refuse while work is in flight.
      onCancel={(event) => {
        event.preventDefault();
        setOpen(false);
      }}
      onClose={() => {
        if (open) setOpen(false);
      }}
      className={cn(
        "m-auto w-[calc(100%-2rem)] max-h-[85dvh] overflow-hidden rounded-xl border border-border bg-card p-0 text-card-foreground",
        "backdrop:bg-black/40 open:animate-in open:fade-in open:zoom-in-95 open:duration-150",
        DIALOG_SIZES[size],
      )}
    >
      {open && (
        <div
          className={cn("relative grid max-h-[85dvh] grid-cols-1 content-start gap-4 overflow-y-auto p-6", className)}
          {...props}
        >
          {children}
          {!hideCloseButton && (
            <button
              type="button"
              onClick={() => setOpen(false)}
              aria-label="닫기"
              className="absolute right-4 top-4 rounded-md p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            >
              <X className="h-4 w-4" aria-hidden="true" />
            </button>
          )}
        </div>
      )}
    </dialog>
  );
}

export function DialogHeader({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex flex-col gap-1.5 pr-8 text-left", className)} {...props} />;
}

export function DialogFooter({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("mt-2 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end", className)}
      {...props}
    />
  );
}

export function DialogTitle({ className, ...props }: React.HTMLAttributes<HTMLHeadingElement>) {
  const { titleId } = useDialog();
  return <h2 id={titleId} className={cn("text-base font-semibold leading-6", className)} {...props} />;
}

export function DialogDescription({ className, ...props }: React.HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("text-sm text-muted-foreground", className)} {...props} />;
}
