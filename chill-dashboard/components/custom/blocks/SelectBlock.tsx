"use client";

// Avara UI Select, rebuilt for CHILL on native controls. The Avara listbox had no
// keyboard or ARIA support, so the props stay and the internals changed:
// - single choice is a styled native <select>: arrow keys, type-ahead, Esc, focus
//   return, the platform picker on phones, and it works inside modal dialogs;
// - `multiple` is a button that opens a native popover (top layer, light dismiss,
//   Esc, focus return) holding a group of real checkboxes.
// `variant` and `color` are accepted for compatibility and ignored: one look, theme tokens.
import { cn } from "@/lib/utils";
import { AlertCircle, ChevronDown } from "lucide-react";
import React, { forwardRef, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";

export type SelectColor = "indigo" | "rose" | "emerald" | "zinc" | string;
export type SelectVariant = "default" | "filled" | "underlined" | "glow" | "glass" | "liquid-glass";
export type SelectSize = "sm" | "md" | "lg";

export interface SelectOption {
  label: string;
  value: string;
  /** Ignored: native options hold text only. */
  icon?: React.ElementType;
  /** Options sharing a group are listed under its heading. */
  group?: string;
}

/** Everything the two selection modes share. */
interface SelectBlockBaseProps {
  options: SelectOption[];
  placeholder?: string;
  variant?: SelectVariant;
  color?: SelectColor;
  inputSize?: SelectSize;
  /** Visible label above the control. */
  label?: string;
  helperText?: string;
  error?: string;
  /** Leading content inside the box, e.g. a short label prefix. */
  icon?: React.ReactNode;
  disabled?: boolean;
  className?: string;
  /** Multiple only: a filter box above the options. Single choice uses the native type-ahead. */
  searchable?: boolean;
  /** Accessible name when there is no visible `label`. */
  "aria-label"?: string;
  id?: string;
  name?: string;
  required?: boolean;
}

/** The default: one option at a time, so the value is a plain string. */
export interface SelectBlockSingleProps extends SelectBlockBaseProps {
  multiple?: false;
  value?: string;
  onChange?: (value: string) => void;
}

/** `multiple` — value and callback both deal in arrays. */
export interface SelectBlockMultipleProps extends SelectBlockBaseProps {
  multiple: true;
  value?: string[];
  onChange?: (value: string[]) => void;
}

/**
 * Discriminated on `multiple`, so a single select hands your callback a
 * `string` and a multi select hands it a `string[]`.
 */
export type SelectBlockProps = SelectBlockSingleProps | SelectBlockMultipleProps;

type SelectBlockImplProps = SelectBlockBaseProps & {
  multiple?: boolean;
  value?: string | string[];
  onChange?: (value: string | string[]) => void;
};

const heights: Record<SelectSize, string> = { sm: "h-8", md: "h-9", lg: "h-10" };
type Naming = { "aria-label"?: string; "aria-labelledby"?: string };

/** Options in order, under their group headings. */
function grouped(options: SelectOption[]) {
  const groups = new Map<string, SelectOption[]>();
  for (const option of options) {
    const key = option.group ?? "";
    groups.set(key, [...(groups.get(key) ?? []), option]);
  }
  return [...groups];
}

const SelectBlockImpl = forwardRef<HTMLDivElement, SelectBlockImplProps>((props, ref) => {
  const { options = [], value, onChange, placeholder = "선택", inputSize = "md", label, helperText, error, icon, disabled = false, className, multiple = false, searchable = false, id, name, required } = props;
  const auto = useId();
  const controlId = id ?? `${auto}-control`;
  const iconId = `${auto}-icon`;
  const noteId = `${auto}-note`;
  const note = error || helperText;
  // Name: the visible label, else aria-label, else the visible prefix, else the placeholder.
  const naming = label ? {} : props["aria-label"] ? { "aria-label": props["aria-label"] } : icon ? { "aria-labelledby": iconId } : { "aria-label": placeholder };
  const frame = cn(
    "relative flex w-full min-w-0 items-center gap-2 rounded-md border bg-card pl-3 text-sm transition-colors",
    "has-[>:is(select,button):focus-visible]:outline-2 has-[>:is(select,button):focus-visible]:outline-offset-2 has-[>:is(select,button):focus-visible]:outline-ring",
    heights[inputSize],
    error ? "border-fail" : "border-input hover:border-foreground/30",
    disabled && "cursor-not-allowed opacity-50",
  );
  const control = "h-full min-w-0 flex-1 cursor-pointer truncate bg-transparent pr-8 text-left outline-none disabled:cursor-not-allowed";
  const chevron = <ChevronDown aria-hidden="true" className="pointer-events-none absolute right-2.5 size-4 text-muted-foreground" />;

  return (
    <div ref={ref} className={cn("flex w-full min-w-0 flex-col gap-1.5", className)}>
      {label && <label htmlFor={controlId} className={cn("text-sm font-medium", error ? "text-fail" : "text-foreground", disabled && "opacity-50")}>{label}</label>}
      {multiple ? (
        <MultiSelect
          frame={frame} control={control} chevron={chevron} controlId={controlId} naming={naming} icon={icon} iconId={iconId}
          options={options} value={Array.isArray(value) ? value : []} onChange={next => onChange?.(next)}
          placeholder={placeholder} disabled={disabled} searchable={searchable} legend={label ?? props["aria-label"] ?? placeholder}
          describedBy={note ? noteId : undefined}
        />
      ) : (
        <div className={frame}>
          {icon && <span id={iconId} className="shrink-0 text-muted-foreground">{icon}</span>}
          <SingleSelect
            className={control} id={controlId} name={name} required={required} disabled={disabled} naming={naming}
            options={options} value={typeof value === "string" ? value : ""} onChange={next => onChange?.(next)}
            placeholder={placeholder} describedBy={note ? noteId : undefined} invalid={!!error}
          />
          {chevron}
        </div>
      )}
      {note && (
        <p id={noteId} className={cn("flex items-center gap-1 text-xs", error ? "font-medium text-fail" : "text-muted-foreground")}>
          {error && <AlertCircle className="size-3.5 shrink-0" aria-hidden="true" />}{note}
        </p>
      )}
    </div>
  );
});
SelectBlockImpl.displayName = "SelectBlock";

function SingleSelect({ options, value, onChange, placeholder, naming, describedBy, invalid, className, ...rest }: {
  options: SelectOption[]; value: string; onChange: (value: string) => void; placeholder: string
  naming: Naming; describedBy?: string; invalid: boolean; className: string
  id: string; name?: string; required?: boolean; disabled: boolean
}) {
  const known = options.some(option => option.value === value);
  return (
    <select
      {...rest} {...naming}
      value={known ? value : ""}
      onChange={event => onChange(event.target.value)}
      aria-invalid={invalid || undefined}
      aria-describedby={describedBy}
      className={cn(className, "appearance-none [&_option]:bg-card [&_option]:text-foreground", !known && "text-muted-foreground")}
    >
      {!known && <option value="" disabled>{placeholder}</option>}
      {grouped(options).map(([group, items]) => {
        const list = items.map(option => <option key={option.value} value={option.value}>{option.label}</option>);
        return group ? <optgroup key={group} label={group}>{list}</optgroup> : <React.Fragment key="">{list}</React.Fragment>;
      })}
    </select>
  );
}

function MultiSelect({ frame, control, chevron, controlId, naming, icon, iconId, options, value, onChange, placeholder, disabled, searchable, legend, describedBy }: {
  frame: string; control: string; chevron: React.ReactNode; controlId: string; naming: Naming
  icon: React.ReactNode; iconId: string; options: SelectOption[]; value: string[]; onChange: (value: string[]) => void
  placeholder: string; disabled: boolean; searchable: boolean; legend: string; describedBy?: string
}) {
  const popoverId = useId();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const shown = useMemo(() => (query ? options.filter(option => option.label.toLowerCase().includes(query.toLowerCase())) : options), [options, query]);
  const labels = options.filter(option => value.includes(option.value)).map(option => option.label);
  const summary = labels.length === 0 ? placeholder : labels.length <= 2 ? labels.join(", ") : `${labels[0]} 외 ${labels.length - 1}`;

  // Native popovers centre in the viewport; tether this one under its trigger, flipping up when below is short.
  const place = useCallback(() => {
    const trigger = triggerRef.current?.parentElement, popover = popoverRef.current;
    if (!trigger || !popover) return;
    const box = trigger.getBoundingClientRect();
    const height = popover.offsetHeight;
    const below = window.innerHeight - box.bottom - 8;
    const top = below < height && box.top - 8 > below ? box.top - height - 4 : box.bottom + 4;
    Object.assign(popover.style, { top: `${Math.max(4, top)}px`, left: `${Math.min(box.left, window.innerWidth - Math.max(box.width, 224) - 4)}px`, width: `${Math.max(box.width, 224)}px` });
  }, []);
  useEffect(() => {
    if (!open) return;
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [open, place]);

  return (
    <div className={frame}>
      {icon && <span id={iconId} className="shrink-0 text-muted-foreground">{icon}</span>}
      <button
        ref={triggerRef} id={controlId} type="button" popoverTarget={popoverId} disabled={disabled}
        {...naming} aria-describedby={describedBy}
        className={cn(control, labels.length === 0 && "text-muted-foreground")}
      >
        {summary}
      </button>
      {chevron}
      <div
        ref={popoverRef} id={popoverId} popover="auto"
        onBeforeToggle={event => { if (event.newState === "open") place(); }}
        onToggle={event => {
          const next = event.newState === "open";
          setOpen(next);
          if (next) { place(); popoverRef.current?.querySelector("input")?.focus(); } else setQuery("");
        }}
        className="inset-auto m-0 overflow-hidden rounded-md border border-border bg-card p-0 text-sm text-foreground shadow-md"
      >
        {searchable && (
          <div className="border-b border-border p-2">
            <input type="search" value={query} onChange={event => setQuery(event.target.value)} aria-label="검색" placeholder="검색" className="h-8 w-full rounded-md border border-input bg-card px-2 text-sm outline-none focus-visible:border-primary" />
          </div>
        )}
        <fieldset className="max-h-60 min-w-0 overflow-y-auto p-1">
          <legend className="sr-only">{legend}</legend>
          {shown.length === 0 ? <p className="px-2 py-2 text-muted-foreground">결과 없음</p> : grouped(shown).map(([group, items]) => (
            <div key={group} role={group ? "group" : undefined} aria-label={group || undefined}>
              {group && <div aria-hidden="true" className="px-2 pb-1 pt-2 text-xs font-medium text-muted-foreground">{group}</div>}
              {items.map(option => {
                const checked = value.includes(option.value);
                return (
                  <label key={option.value} className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 hover:bg-muted has-[:focus-visible]:bg-muted">
                    <input type="checkbox" className="size-4 shrink-0 accent-primary" checked={checked} onChange={() => onChange(checked ? value.filter(item => item !== option.value) : [...value, option.value])} />
                    <span className="min-w-0 truncate">{option.label}</span>
                  </label>
                );
              })}
            </div>
          ))}
        </fieldset>
      </div>
    </div>
  );
}

export const SelectBlock = SelectBlockImpl as React.ForwardRefExoticComponent<SelectBlockProps & React.RefAttributes<HTMLDivElement>>;
