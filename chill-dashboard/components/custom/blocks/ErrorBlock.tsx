"use client";

// Avara UI Error section, reduced for CHILL: the terminal, radar, pipeline and
// glitch variants were removed. What remains is the error code, title, the
// incident id with copy, and the retry and home actions, on the theme tokens.
import React from "react";
import { House, RotateCcw } from "lucide-react";
import { cn } from "@/lib/utils";
import { CopyButton } from "./CopyButtonBlock";

export interface ErrorSectionProps extends React.HTMLAttributes<HTMLElement> {
  /** Primary error code or status, e.g. "500". */
  errorCode?: string;
  title?: string;
  /** Digest/incident id, shown with a copy button. */
  errorId?: string;
  onRetry?: () => void;
  onHome?: () => void;
}

export function ErrorSection({ errorCode = "오류", title = "화면을 표시하지 못함", errorId, onRetry, onHome, className, ...props }: ErrorSectionProps) {
  const button = "inline-flex h-9 items-center justify-center gap-1.5 rounded-md border px-3.5 text-sm font-medium transition-colors [&_svg]:size-4";
  return (
    <section role="alert" className={cn("flex min-h-[60dvh] w-full flex-col items-center justify-center px-4 py-12 text-center", className)} {...props}>
      <p className="text-5xl font-semibold tracking-tight text-fail/70">{errorCode}</p>
      <h1 className="mt-3 text-xl font-semibold text-foreground">{title}</h1>
      {errorId && (
        <p className="mt-2 inline-flex items-center gap-1 font-mono text-xs text-muted-foreground">
          {errorId}
          <CopyButton value={errorId} iconOnly variant="ghost" size="sm" aria-label="오류 번호 복사" className="size-6" />
        </p>
      )}
      <div className="mt-6 flex flex-wrap items-center justify-center gap-2">
        {onRetry && <button type="button" onClick={onRetry} className={cn(button, "border-transparent bg-primary text-primary-foreground hover:bg-primary/90")}><RotateCcw aria-hidden="true" />다시 시도</button>}
        {onHome && <button type="button" onClick={onHome} className={cn(button, "border-border bg-card hover:bg-muted")}><House aria-hidden="true" />개요로</button>}
      </div>
    </section>
  );
}

export default ErrorSection;
