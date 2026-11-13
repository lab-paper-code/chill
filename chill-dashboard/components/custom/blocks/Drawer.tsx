"use client";

// Avara UI Drawer, patched for CHILL: a native modal <dialog> pinned to one
// edge. The browser handles focus containment, Esc and focus return; the page
// behind becomes inert. The overlay is the dialog's ::backdrop (clicking it
// closes). The demo Button export and DrawerOverlay were removed.
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React, { createContext, useContext, useEffect, useId, useRef } from "react";
import { dialogOpener, refocusOpener } from "./dialog-context";

type DrawerSide = "top" | "bottom" | "left" | "right";

const DrawerContext = createContext<{ titleId: string; close: () => void } | null>(null);

const SIDES: Record<DrawerSide, string> = {
  right: "ml-auto mr-0 h-dvh max-h-dvh w-[440px] max-w-[92vw] border-l open:slide-in-from-right",
  left: "mr-auto ml-0 h-dvh max-h-dvh w-[440px] max-w-[92vw] border-r open:slide-in-from-left",
  top: "mt-0 mb-auto w-full max-w-none max-h-[85dvh] border-b open:slide-in-from-top",
  bottom: "mb-0 mt-auto w-full max-w-none max-h-[85dvh] border-t open:slide-in-from-bottom",
};

export function Drawer({
  open,
  onOpenChange,
  side = "right",
  className,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  side?: DrawerSide;
  className?: string;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const titleId = useId();

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) {
      opener.current = dialogOpener();
      dialog.showModal();
    } else if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        onOpenChange(false);
      }}
      onClose={() => {
        refocusOpener(opener.current);
        if (open) onOpenChange(false);
      }}
      // A click on the dialog box itself (not its content) is a click on the backdrop.
      onClick={(event) => {
        if (event.target === event.currentTarget) onOpenChange(false);
      }}
      className={cn(
        "border-border bg-card p-0 text-card-foreground backdrop:bg-black/30",
        "open:animate-in open:duration-200",
        SIDES[side],
        className,
      )}
    >
      {open && (
        <DrawerContext.Provider value={{ titleId, close: () => onOpenChange(false) }}>
          {children}
        </DrawerContext.Provider>
      )}
    </dialog>
  );
}

export const DrawerContent = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(
  ({ className, children, ...props }, ref) => {
    const context = useContext(DrawerContext);
    return (
      <div ref={ref} className={cn("relative flex h-full flex-col overflow-hidden", className)} {...props}>
        <button
          type="button"
          onClick={context?.close}
          aria-label="닫기"
          className="absolute right-3 top-3 z-10 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <X className="h-4 w-4" aria-hidden="true" />
        </button>
        {children}
      </div>
    );
  },
);
DrawerContent.displayName = "DrawerContent";

export const DrawerHeader = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => (
  <div className={cn("shrink-0 border-b border-border px-5 py-4 pr-12", className)} {...props} />
);

export const DrawerTitle = ({ className, ...props }: React.HTMLAttributes<HTMLHeadingElement>) => {
  const context = useContext(DrawerContext);
  return <h2 id={context?.titleId} className={cn("truncate text-base font-semibold text-foreground", className)} {...props} />;
};

export const DrawerDescription = ({ className, ...props }: React.HTMLAttributes<HTMLParagraphElement>) => (
  <p className={cn("mt-1 text-sm text-muted-foreground", className)} {...props} />
);

export const DrawerFooter = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => (
  <div
    className={cn("flex shrink-0 items-center justify-end gap-2 border-t border-border px-5 py-3", className)}
    {...props}
  />
);
