"use client";

// Avara UI Card, trimmed for CHILL: the primitives only (the ProjectCard and
// ProductCard showcases were removed). Depth is a 1px border, never a shadow.
import { cn } from "@/lib/utils";
import React from "react";

export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: "default" | "bordered" | "flat";
  interactive?: boolean;
}

export function Card({
  className,
  variant = "bordered",
  interactive = false,
  children,
  ...props
}: CardProps) {
  const variants = {
    default: "bg-card border border-border",
    bordered: "bg-card border border-border",
    flat: "bg-muted border border-border",
  };

  return (
    <div
      className={cn(
        "rounded-xl text-card-foreground flex flex-col min-w-0",
        variants[variant],
        interactive && "cursor-pointer transition-colors hover:border-primary/40",
        className,
      )}
      {...props}
    >
      {children}
    </div>
  );
}

export function CardHeader({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("flex flex-col gap-1 px-5 pt-4 pb-3", className)} {...props}>
      {children}
    </div>
  );
}

export function CardTitle({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLHeadingElement>) {
  return (
    <h3 className={cn("text-sm font-semibold leading-5 text-foreground", className)} {...props}>
      {children}
    </h3>
  );
}

export function CardDescription({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLParagraphElement>) {
  return (
    <p className={cn("text-sm text-muted-foreground", className)} {...props}>
      {children}
    </p>
  );
}

export function CardContent({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("px-5 pb-5 flex-1 min-w-0", className)} {...props}>
      {children}
    </div>
  );
}

export function CardFooter({
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("flex items-center gap-2 px-5 pb-4 mt-auto", className)} {...props}>
      {children}
    </div>
  );
}
