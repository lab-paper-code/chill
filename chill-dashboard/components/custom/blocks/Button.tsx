"use client";

// Avara UI Button, trimmed for CHILL: the six plain variants only. The
// decorative variants (spinning borders, ripples, shards tiles, pricing
// looks) and the colour presets were removed; colour comes from theme tokens.
import { cn } from "@/lib/utils";
import { Loader2 } from "lucide-react";
import React from "react";

export type ButtonVariant =
  | "default"
  | "outline"
  | "secondary"
  | "destructive"
  | "ghost"
  | "link";
export type ButtonSize = "default" | "sm" | "lg" | "icon";

const VARIANTS: Record<ButtonVariant, string> = {
  default: "bg-primary text-primary-foreground border-transparent hover:bg-primary/90",
  outline: "bg-card text-foreground border-border hover:bg-muted",
  secondary: "bg-muted text-foreground border-transparent hover:bg-muted/70",
  destructive: "bg-fail text-white border-transparent hover:bg-fail/90",
  ghost: "bg-transparent text-foreground border-transparent hover:bg-muted",
  link: "bg-transparent text-primary border-transparent underline-offset-4 hover:underline px-0 h-auto",
};

const SIZES: Record<ButtonSize, string> = {
  default: "h-9 px-3.5 text-sm",
  sm: "h-8 px-3 text-sm",
  lg: "h-10 px-4 text-sm",
  icon: "size-8",
};

/** Class string of a button, for links that should look like one. */
export function buttonClassName({
  variant = "outline",
  size = "default",
  className,
}: { variant?: ButtonVariant; size?: ButtonSize; className?: string } = {}) {
  return cn(
    "inline-flex shrink-0 select-none items-center justify-center gap-1.5 whitespace-nowrap rounded-md border font-medium transition-colors cursor-pointer",
    "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
    "disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0",
    VARIANTS[variant],
    SIZES[size],
    className,
  );
}

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Shows a spinner in place of the leading icon and disables the button. */
  loading?: boolean;
  iconLeft?: React.ReactNode;
  iconRight?: React.ReactNode;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  (
    {
      className,
      variant = "outline",
      size = "default",
      type = "button",
      loading,
      disabled,
      iconLeft,
      iconRight,
      children,
      ...props
    },
    ref,
  ) => (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={buttonClassName({ variant, size, className })}
      {...props}
    >
      {loading ? <Loader2 className="animate-spin" aria-hidden="true" /> : iconLeft}
      {children}
      {iconRight}
    </button>
  ),
);
Button.displayName = "Button";

export default Button;
