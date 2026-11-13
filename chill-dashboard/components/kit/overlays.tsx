"use client"

import * as React from "react"
import { Download } from "lucide-react"
import { cn } from "@/lib/utils"
import { stringifySchedulerJSON } from "@/lib/scheduler"
import { Drawer, DrawerContent, DrawerFooter, DrawerHeader, DrawerTitle } from "@/components/custom/blocks/Drawer"
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/custom/blocks/DialogBlock"
import Button from "@/components/custom/blocks/Button"
import { CopyButton } from "@/components/custom/blocks/CopyButtonBlock"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import type { StatusInfo } from "@/lib/status-labels"
import { StatusBadge } from "./display"

/** Row preview from the right edge. One at a time; never stacked. */
export function DetailDrawer({ open, onOpenChange, title, status, actions, footer, children }: {
  open: boolean; onOpenChange: (open: boolean) => void
  title: string; status?: StatusInfo
  /** Buttons in the header, e.g. "상세로 이동". */ actions?: React.ReactNode
  footer?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <Drawer open={open} onOpenChange={onOpenChange} side="right">
      <DrawerContent>
        <DrawerHeader className="flex flex-wrap items-center gap-2">
          <DrawerTitle className="min-w-0 whitespace-normal [overflow-wrap:anywhere]">{title}</DrawerTitle>
          {status && <StatusBadge status={status} />}
          {actions && <div className="ml-auto flex items-center gap-2">{actions}</div>}
        </DrawerHeader>
        <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">{children}</div>
        {footer && <DrawerFooter>{footer}</DrawerFooter>}
      </DrawerContent>
    </Drawer>
  )
}

export type Change = { label: string; before: React.ReactNode; after: React.ReactNode }

/**
 * Confirmation for a change or a dangerous action: target, before and after, optional required reason.
 * `onConfirm` runs the mutation; the dialog stays open while `pending` and the caller closes it.
 */
type ConfirmProps = {
  open: boolean; onOpenChange: (open: boolean) => void
  title: string
  /** What is changed, e.g. the service name. */ target?: React.ReactNode
  changes?: Change[]
  /** Extra short content (impact). */ children?: React.ReactNode
  confirmLabel?: string
  tone?: "default" | "destructive"
  /** Asks for a reason; `required` blocks confirmation while it is empty. */
  reason?: { label?: string; required?: boolean; maxLength?: number }
  pending?: boolean
  onConfirm: (reason: string) => void
}
export function ConfirmDialog(props: ConfirmProps) {
  return (
    <Dialog open={props.open} onOpenChange={next => { if (!props.pending) props.onOpenChange(next) }}>
      <DialogContent size="md" hideCloseButton={props.pending}>
        <ConfirmBody {...props} />
      </DialogContent>
    </Dialog>
  )
}
// Mounted only while open, so the reason starts empty every time.
function ConfirmBody({ onOpenChange, title, target, changes, children, confirmLabel = "확인", tone = "default", reason, pending = false, onConfirm }: ConfirmProps) {
  const [text, setText] = React.useState("")
  const reasonId = React.useId()
  return (
    <>
        <DialogHeader><DialogTitle>{title}</DialogTitle></DialogHeader>
        <form
          className="grid gap-4"
          onSubmit={event => {
            event.preventDefault()
            if (!pending) onConfirm(text.trim())
          }}
        >
          {target && <div className="text-sm [overflow-wrap:anywhere]"><span className="text-muted-foreground">대상 </span><span className="font-medium">{target}</span></div>}
          {changes && changes.length > 0 && (
            // Label column: its content width up to 9rem, Korean words kept whole. Values wrap by word; long tokens (names, ids) break anywhere.
            <div className="rounded-md border border-border">
              <table className="w-full text-sm">
                <caption className="sr-only">변경 전후</caption>
                <thead className="bg-muted/60 text-xs text-muted-foreground">
                  <tr><th scope="col" className="whitespace-nowrap px-3 py-2 text-left font-medium">항목</th><th scope="col" className="whitespace-nowrap px-3 py-2 text-left font-medium">변경 전</th><th scope="col" className="whitespace-nowrap px-3 py-2 text-left font-medium">변경 후</th></tr>
                </thead>
                <tbody>
                  {changes.map(change => (
                    <tr key={change.label} className="border-t border-border align-top">
                      <th scope="row" className="px-3 py-2 text-left font-normal text-muted-foreground"><span className="block max-w-36 break-keep">{change.label}</span></th>
                      <td className="px-3 py-2 tabular-nums break-keep [overflow-wrap:anywhere]">{change.before}</td>
                      <td className="px-3 py-2 font-medium tabular-nums break-keep [overflow-wrap:anywhere]">{change.after}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {children}
          {reason && (
            <div className="grid gap-1.5">
              <Label htmlFor={reasonId}>{reason.label ?? "사유"}</Label>
              <Textarea
                id={reasonId}
                value={text}
                onChange={event => setText(event.target.value)}
                required={reason.required}
                maxLength={reason.maxLength ?? 500}
                rows={3}
                className="user-invalid:border-fail"
              />
            </div>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" disabled={pending} onClick={() => onOpenChange(false)} autoFocus={tone === "destructive"}>취소</Button>
            <Button type="submit" variant={tone === "destructive" ? "destructive" : "default"} loading={pending}>{confirmLabel}</Button>
          </DialogFooter>
        </form>
    </>
  )
}

/** Raw server document: pretty JSON with exact 64-bit integers, copy and download. */
export function RawDialog({ open, onOpenChange, title = "원본 보기", value, filename }: {
  open: boolean; onOpenChange: (open: boolean) => void
  title?: string
  /** Parsed value; bigint fields are kept exact. */ value: unknown
  /** Download name without extension. */ filename: string
}) {
  const text = React.useMemo(() => {
    if (!open) return ""
    try { return stringifySchedulerJSON(value, 2) } catch { return JSON.stringify(value, (_key, item) => typeof item === "bigint" ? item.toString() : item, 2) }
  }, [open, value])
  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: "application/json" }))
    const link = Object.assign(document.createElement("a"), { href: url, download: `${filename}.json` })
    link.click()
    URL.revokeObjectURL(url)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg">
        <DialogHeader><DialogTitle>{title}</DialogTitle></DialogHeader>
        <pre tabIndex={0} aria-label={title} className={cn("max-h-[60dvh] overflow-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-xs leading-5 text-foreground")}>{text}</pre>
        <DialogFooter>
          <CopyButton value={text} variant="outline" size="sm" label="복사" copiedLabel="복사됨" errorLabel="복사 실패" />
          <Button size="sm" variant="outline" iconLeft={<Download />} onClick={download}>내려받기</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
