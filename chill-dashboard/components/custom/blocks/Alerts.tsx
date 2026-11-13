"use client";

// Avara UI Alert, patched for CHILL: one calm look per type drawn from the
// theme tokens (--primary, --ok, --warn, --fail, --idle). The decorative
// variants (glass shine, glow pulse, solid fills) and the entrance animation
// were removed; `variant` is accepted and ignored.
import { cn } from "@/lib/utils";
import { AlertTriangle, Bell, CheckCircle2, Info, X, XCircle } from "lucide-react";
import React, { useState } from "react";

const ICON_MAP: Record<string, React.ElementType> = { CheckCircle2, AlertTriangle, Info, Bell, XCircle };

export type AlertType = "primary" | "secondary" | "success" | "danger" | "warning" | "info" | "dark";
export type AlertVariant = "solid" | "outline" | "modern" | "glass" | "glow";

export interface AlertProps {
  type?: AlertType;
  variant?: AlertVariant;
  dismissible?: boolean;
  icon?: React.ReactNode | string | null;
  title?: string | null;
  message?: string;
  className?: string;
  titleClassName?: string;
  messageClassName?: string;
  iconClassName?: string;
  dismissIconClassName?: string;
  onDismiss?: (() => void) | null;
  children?: React.ReactNode | null;
}

const TONES: Record<AlertType, { box: string; icon: string }> = {
  primary: { box: "border-primary/30 bg-primary-soft", icon: "text-primary" },
  info: { box: "border-primary/30 bg-primary-soft", icon: "text-primary" },
  success: { box: "border-ok/30 bg-ok-soft", icon: "text-ok" },
  warning: { box: "border-warn/30 bg-warn-soft", icon: "text-warn" },
  danger: { box: "border-fail/30 bg-fail-soft", icon: "text-fail" },
  secondary: { box: "border-border bg-muted", icon: "text-muted-foreground" },
  dark: { box: "border-border bg-muted", icon: "text-foreground" },
};

export const Alert: React.FC<AlertProps> = ({
  type = "primary",
  dismissible = false,
  icon = null,
  title = null,
  message = "",
  className = "",
  titleClassName = "",
  messageClassName = "",
  iconClassName = "",
  dismissIconClassName = "",
  onDismiss = null,
  children = null,
}) => {
  const [isVisible, setIsVisible] = useState(true);
  if (!isVisible) return null;
  const tone = TONES[type] ?? TONES.info;
  const Mapped = typeof icon === "string" ? ICON_MAP[icon] : undefined;
  const iconNode = Mapped ? <Mapped className="h-4 w-4" aria-hidden="true" /> : React.isValidElement(icon) ? icon : null;

  return (
    <div
      role={type === "danger" ? "alert" : "status"}
      className={cn("relative flex w-full items-start gap-3 rounded-lg border px-4 py-3 text-sm text-foreground", tone.box, className)}
    >
      {iconNode && <div className={cn("mt-0.5 shrink-0", tone.icon, iconClassName)}>{iconNode}</div>}
      <div className={cn("min-w-0 flex-1", dismissible && "pr-6")}>
        {title && <p className={cn("font-medium", titleClassName)}>{title}</p>}
        {message && <p className={cn("text-muted-foreground", messageClassName)}>{message}</p>}
        {children && <div className="mt-2">{children}</div>}
      </div>
      {dismissible && (
        <button
          type="button"
          onClick={() => {
            onDismiss?.();
            setIsVisible(false);
          }}
          className={cn("absolute right-2 top-2 rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground", dismissIconClassName)}
          aria-label="닫기"
        >
          <X className="h-4 w-4" aria-hidden="true" />
        </button>
      )}
    </div>
  );
};
