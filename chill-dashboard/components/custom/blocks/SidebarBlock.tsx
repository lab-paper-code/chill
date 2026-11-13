"use client";

// Avara UI Sidebar, patched for CHILL:
// - SidebarItem renders a link (`href` + `linkComponent`) with aria-current="page" when active,
//   so navigation stays links in the Tab order; without `href` it stays a button.
// - The closed mobile drawer is `inert`, Esc closes it, and the close control has a name.
// - The desktop rail is sticky to the viewport; group labels are not all-caps.
import { cn } from "@/lib/utils";
import { useIsMobile } from "@/hooks/use-mobile";
import { Menu, X } from "lucide-react";
import React, { useEffect, useState } from "react";
import { SidebarContext, useSidebar } from "./sidebar-context";
import { Tooltip, TooltipContent, TooltipTrigger } from "./TooltipBlock";

export interface SidebarProviderProps {
  children: React.ReactNode;
  defaultExpanded?: boolean;
  color?: string;
  className?: string;
}

export function SidebarProvider({
  children,
  defaultExpanded = true,
  color = "primary",
  className,
}: SidebarProviderProps) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const [isMobileOpen, setIsMobileOpen] = useState(false);

  useEffect(() => {
    const handleResize = () => {
      if (window.innerWidth >= 768) setIsMobileOpen(false);
    };
    window.addEventListener("resize", handleResize);
    return () => window.removeEventListener("resize", handleResize);
  }, []);

  useEffect(() => {
    if (!isMobileOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setIsMobileOpen(false);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [isMobileOpen]);

  return (
    <SidebarContext.Provider value={{ expanded, setExpanded, isMobileOpen, setIsMobileOpen, color }}>
      <div className={cn("relative flex min-h-dvh w-full", className)}>{children}</div>
    </SidebarContext.Provider>
  );
}

export interface SidebarProps extends React.HTMLAttributes<HTMLElement> {
  variant?: "attached" | "floating";
}

export function Sidebar({ variant = "attached", className, children, ...props }: SidebarProps) {
  const { expanded, isMobileOpen, setIsMobileOpen } = useSidebar();
  const mobile = useIsMobile();

  return (
    <>
      <div
        aria-hidden="true"
        onClick={() => setIsMobileOpen(false)}
        className={cn(
          "fixed inset-0 z-40 bg-black/40 md:hidden",
          "transition-[opacity,visibility] duration-300 motion-reduce:transition-none",
          isMobileOpen ? "visible opacity-100" : "invisible opacity-0",
        )}
      />
      <aside
        inert={mobile && !isMobileOpen ? true : undefined}
        className={cn(
          "fixed inset-y-0 left-0 z-50 flex flex-col md:sticky md:top-0 md:h-dvh md:translate-x-0",
          "transition-[width,transform] duration-300 ease-out motion-reduce:transition-none",
          expanded ? "w-60" : "w-60 md:w-16",
          isMobileOpen ? "translate-x-0" : "-translate-x-full",
          variant === "attached"
            ? "border-r border-border bg-card text-card-foreground"
            : "m-4 h-[calc(100dvh-2rem)] rounded-xl border border-border bg-card text-card-foreground",
          className,
        )}
        {...props}
      >
        {children}
      </aside>
    </>
  );
}

export function SidebarHeader({ className, children, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  const { expanded, setIsMobileOpen } = useSidebar();
  return (
    <div
      className={cn(
        "flex h-14 shrink-0 items-center justify-between px-4",
        !expanded && "md:justify-center md:px-0",
        className,
      )}
      {...props}
    >
      {children}
      <button
        type="button"
        onClick={() => setIsMobileOpen(false)}
        aria-label="메뉴 닫기"
        className="rounded-md p-1 text-muted-foreground hover:bg-muted md:hidden"
      >
        <X className="h-5 w-5" aria-hidden="true" />
      </button>
    </div>
  );
}

export function SidebarBrand({
  icon,
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement> & { icon?: React.ReactNode }) {
  const { expanded } = useSidebar();
  return (
    <div className={cn("flex items-center text-base font-semibold text-foreground", className)} {...props}>
      {icon && <div className="shrink-0">{icon}</div>}
      <div
        className={cn(
          "ml-2 overflow-hidden whitespace-nowrap transition-all duration-300",
          !expanded && "md:ml-0 md:max-w-0 md:opacity-0",
        )}
      >
        {children}
      </div>
    </div>
  );
}

export function SidebarFooter({ className, children, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("mt-auto flex shrink-0 flex-col border-t border-border p-2", className)} {...props}>
      {children}
    </div>
  );
}

export function SidebarContent({ className, children, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={cn("flex-1 space-y-5 overflow-y-auto overflow-x-hidden px-2 py-3", className)} {...props}>
      {children}
    </div>
  );
}

export function SidebarGroup({
  label,
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLDivElement> & { label?: string }) {
  const { expanded } = useSidebar();
  return (
    <div className={cn("flex flex-col gap-0.5", className)} {...props}>
      {label && (
        <div
          className={cn(
            "mb-1 overflow-hidden px-3 text-xs font-medium text-muted-foreground transition-all duration-300",
            !expanded && "md:mb-0 md:max-h-0 md:opacity-0",
          )}
        >
          {label}
        </div>
      )}
      <ul className="flex flex-col gap-0.5">{children}</ul>
    </div>
  );
}

export interface SidebarItemProps extends React.HTMLAttributes<HTMLElement> {
  icon?: React.ReactNode;
  active?: boolean;
  /** Renders a link to this address instead of a button. */
  href?: string;
  /** Element used for links, e.g. next/link. Defaults to `a`. */
  linkComponent?: React.ElementType;
}

export function SidebarItem({
  icon,
  active,
  href,
  linkComponent: Link = "a",
  className,
  children,
  ...props
}: SidebarItemProps) {
  const { expanded, setIsMobileOpen } = useSidebar();
  const classes = cn(
    "group relative flex h-9 w-full items-center rounded-md px-3 text-sm font-medium transition-colors",
    !expanded && "md:justify-center md:px-0",
    active ? "bg-primary-soft text-primary" : "text-muted-foreground hover:bg-muted hover:text-foreground",
    className,
  );
  const body = (
    <>
      {icon && <span className="shrink-0 [&_svg]:size-[18px]" aria-hidden="true">{icon}</span>}
      <span
        className={cn(
          "ml-3 overflow-hidden whitespace-nowrap text-left transition-all duration-300",
          !expanded && "md:ml-0 md:max-w-0 md:opacity-0",
        )}
      >
        {children}
      </span>
    </>
  );
  const item = href ? (
    <Link
      href={href}
      aria-current={active ? "page" : undefined}
      onClick={() => setIsMobileOpen(false)}
      className={classes}
      {...props}
    >
      {body}
    </Link>
  ) : (
    <button type="button" className={classes} {...props}>
      {body}
    </button>
  );

  // Collapsed rail: the label moves into a tooltip; it portals, so the rail's overflow never clips it.
  if (expanded) return <li>{item}</li>;
  return (
    <li>
      <Tooltip delayDuration={100}>
        <TooltipTrigger className="block w-full">{item}</TooltipTrigger>
        <TooltipContent side="right" sideOffset={10} className="hidden md:block">
          {children}
        </TooltipContent>
      </Tooltip>
    </li>
  );
}

export function SidebarTrigger({ className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  const { expanded, setExpanded } = useSidebar();
  return (
    <button
      type="button"
      onClick={() => setExpanded(!expanded)}
      aria-label={expanded ? "메뉴 접기" : "메뉴 펼치기"}
      aria-expanded={expanded}
      className={cn("hidden rounded-md p-2 text-muted-foreground transition-colors hover:bg-muted md:flex", className)}
      {...props}
    >
      <Menu className="h-5 w-5" aria-hidden="true" />
    </button>
  );
}

export function MobileSidebarTrigger({ className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  const { isMobileOpen, setIsMobileOpen } = useSidebar();
  return (
    <button
      type="button"
      onClick={() => setIsMobileOpen(true)}
      aria-label="메뉴 열기"
      aria-expanded={isMobileOpen}
      className={cn("rounded-md p-2 text-muted-foreground transition-colors hover:bg-muted md:hidden", className)}
      {...props}
    >
      <Menu className="h-5 w-5" aria-hidden="true" />
    </button>
  );
}
