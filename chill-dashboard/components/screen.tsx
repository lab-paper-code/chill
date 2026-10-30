"use client"

import * as React from "react"
import Link from "next/link"
import { ChevronLeft, ChevronRight } from "lucide-react"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"

// Forms stay narrow; measurement tables opt into the available data width.
export function Screen({back,status,title,lede,children,action,width="narrow",actionPlacement="footer",breadcrumbs}:{back?:[href:string,label:string];status?:React.ReactNode;title:React.ReactNode;lede?:React.ReactNode;children?:React.ReactNode;action?:React.ReactNode;width?:"narrow"|"data";actionPlacement?:"footer"|"header";breadcrumbs?:{href?:string;label:string}[]}) {
  return <div className={`screen${width === "data" ? " screen-data" : ""}`}>
    {breadcrumbs && breadcrumbs.length > 1 && <nav aria-label="현재 위치"><ol className="screen-breadcrumbs">{breadcrumbs.map((item,index) => <li key={index}>{index > 0 && <ChevronRight aria-hidden="true" />}{item.href && index < breadcrumbs.length - 1 ? <Link href={item.href}>{item.label}</Link> : <span aria-current={index === breadcrumbs.length - 1 ? "page" : undefined}>{item.label}</span>}</li>)}</ol></nav>}
    {back && !breadcrumbs && <Link className="screen-back" href={back[0]}><ChevronLeft aria-hidden="true" />{back[1]}</Link>}
    <header className="screen-head"><div className="screen-heading">{status}<h1>{title}</h1>{lede && <p>{lede}</p>}</div>{action && actionPlacement === "header" && <div className="screen-header-action">{action}</div>}</header>
    {children}
    {action && actionPlacement === "footer" && <div className="screen-action">{action}</div>}
  </div>
}

// The single key number of a screen.
export function Figure({label,value,unit,note}:{label:string;value:React.ReactNode;unit?:string;note?:React.ReactNode}) {
  return <div className="figure"><span className="figure-label">{label}</span><span className="figure-value">{value}{unit && <small>{unit}</small>}</span>{note && <span className="figure-note">{note}</span>}</div>
}

// A grouped list of rows. Each row either moves to the next screen or opens one dialog.
export function Rows({label,children}:{label?:string;children:React.ReactNode}) {
  return <section className="rows-group" aria-label={label}>{label && <h2 className="rows-label">{label}</h2>}<ul className="rows">{children}</ul></section>
}

function RowBody({label,hint,value}:{label:React.ReactNode;hint?:React.ReactNode;value?:React.ReactNode}) {
  return <><span className="row-main"><span className="row-text">{label}</span>{hint && <span className="row-hint">{hint}</span>}</span>{value !== undefined && value !== null && <span className="row-value">{value}</span>}<ChevronRight className="row-chevron" aria-hidden="true" /></>
}

export function Row({href,label,hint,value,onClick}:{href?:string;label:React.ReactNode;hint?:React.ReactNode;value?:React.ReactNode;onClick?:()=>void}) {
  return <li>{href ? <Link className="row" href={href} scroll={false}><RowBody label={label} hint={hint} value={value} /></Link> : <button type="button" className="row" onClick={onClick}><RowBody label={label} hint={hint} value={value} /></button>}</li>
}

// A row whose dependent detail opens in a dialog instead of another panel on the same screen.
export function RowDialog({label,hint,value,title,description,children,open,onOpenChange}:{label:React.ReactNode;hint?:React.ReactNode;value?:React.ReactNode;title:string;description?:string;children:React.ReactNode;open?:boolean;onOpenChange?:(open:boolean)=>void}) {
  return <li><Dialog open={open} onOpenChange={onOpenChange}><DialogTrigger asChild><button type="button" className="row"><RowBody label={label} hint={hint} value={value} /></button></DialogTrigger>
    <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-xl">
      <DialogHeader><DialogTitle>{title}</DialogTitle>{description ? <DialogDescription>{description}</DialogDescription> : <DialogDescription className="sr-only">{title}의 자세한 내용</DialogDescription>}</DialogHeader>
      <div className="min-w-0 space-y-4">{children}</div>
    </DialogContent>
  </Dialog></li>
}

// Read-only label/value pairs, one per line.
export function Facts({label,items}:{label?:string;items:[label:React.ReactNode,value:React.ReactNode][]}) {
  const list = <dl className="facts-list">{items.map(([name,value],index) => <div key={index}><dt>{name}</dt><dd>{value}</dd></div>)}</dl>
  return label ? <section className="rows-group" aria-label={label}><h2 className="rows-label">{label}</h2>{list}</section> : list
}

// Nothing to show yet: say why and what to do next.
export function Empty({title,children}:{title:string;children?:React.ReactNode}) {
  return <div className="screen-empty"><p className="screen-empty-title">{title}</p>{children}</div>
}
