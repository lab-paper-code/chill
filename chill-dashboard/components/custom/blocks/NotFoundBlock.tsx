"use client";

// Avara UI Not Found section, reduced for CHILL to the classic layout without
// the ambient glows, grid backdrop, spinning badge, serif type and shortcuts:
// code, title and up to two actions on the theme tokens.
import React from "react";
import { cn } from "@/lib/utils";

export interface NotFoundAction {
  label: string;
  href?: string;
  onClick?: () => void;
  icon?: React.ReactNode;
}

export interface NotFoundSectionProps extends React.HTMLAttributes<HTMLElement> {
  /** Large code, defaults to "404". */
  code?: string;
  title?: string;
  primaryAction?: NotFoundAction;
  secondaryAction?: NotFoundAction;
  /** Element used for href actions, e.g. next/link. */
  linkComponent?: React.ElementType;
}

export function NotFoundSection({
  code = "404",
  title = "페이지 없음",
  primaryAction,
  secondaryAction,
  linkComponent: Link = "a",
  className,
  ...props
}: NotFoundSectionProps) {
  const action = (item: NotFoundAction | undefined, primary: boolean) => {
    if (!item) return null;
    const classes = cn(
      "inline-flex h-9 items-center justify-center gap-1.5 rounded-md border px-3.5 text-sm font-medium transition-colors [&_svg]:size-4",
      primary ? "border-transparent bg-primary text-primary-foreground hover:bg-primary/90" : "border-border bg-card text-foreground hover:bg-muted",
    );
    return item.href ? (
      <Link href={item.href} className={classes}>{item.icon}{item.label}</Link>
    ) : (
      <button type="button" onClick={item.onClick} className={classes}>{item.icon}{item.label}</button>
    );
  };
  return (
    <section className={cn("flex min-h-[60dvh] w-full flex-col items-center justify-center px-4 py-12 text-center", className)} {...props}>
      <p className="text-6xl font-semibold tabular-nums tracking-tight text-muted-foreground/60">{code}</p>
      <h1 className="mt-3 text-xl font-semibold text-foreground">{title}</h1>
      {(primaryAction || secondaryAction) && (
        <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
          {action(primaryAction, true)}
          {action(secondaryAction, false)}
        </div>
      )}
    </section>
  );
}

export default NotFoundSection;
