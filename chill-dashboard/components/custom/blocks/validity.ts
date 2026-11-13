"use client";

// CHILL: native constraint validation stays (required, min, max… keep their semantics for assistive
// tech), but the browser's own bubble, in the browser's language, is never shown. Kit controls show
// the Korean message below the field; any other control gets the Korean text as its custom validity.

type Field = HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;

/** Korean text for the constraint a control fails. */
export function validityMessage(field: Field): string {
  const v = field.validity;
  if (v.customError) return field.validationMessage;
  if (v.valueMissing) return field instanceof HTMLSelectElement ? "선택 필요" : "입력 필요";
  if (v.badInput) return field instanceof HTMLInputElement && field.type === "number" ? "숫자 아님" : "형식 오류";
  if (v.typeMismatch || v.patternMismatch) return "형식 오류";
  if (v.rangeUnderflow) return `최소 ${(field as HTMLInputElement).min}`;
  if (v.rangeOverflow) return `최대 ${(field as HTMLInputElement).max}`;
  if (v.stepMismatch) return `${(field as HTMLInputElement).step} 단위`;
  if (v.tooShort) return `최소 ${(field as HTMLInputElement).minLength}자`;
  if (v.tooLong) return `최대 ${(field as HTMLInputElement).maxLength}자`;
  return "값 확인 필요";
}

/**
 * `onInvalid` for a kit control that shows its own message: no bubble, `show(message)`, and focus on the
 * form's first invalid control (the browser skips that once the event is cancelled).
 */
export function ownInvalid<E extends Field>(show: (message: string) => void, outer?: React.FormEventHandler<E>): React.FormEventHandler<E> {
  return (event) => {
    outer?.(event);
    event.preventDefault();
    const field = event.currentTarget;
    show(validityMessage(field));
    const first = field.form?.querySelector<HTMLElement>(":is(input, select, textarea):invalid");
    if (!first || first === field) field.focus();
  };
}

/** Document-wide fallback for controls outside the kit: their bubble says the Korean text. */
export function koreanValidity(): () => void {
  const isField = (target: EventTarget | null): target is Field =>
    target instanceof HTMLInputElement || target instanceof HTMLSelectElement || target instanceof HTMLTextAreaElement;
  const invalid = (event: Event) => {
    const field = event.target;
    if (!isField(field) || field.hasAttribute("data-own-validity") || field.validity.customError) return;
    field.setCustomValidity(validityMessage(field));
    field.dataset.koValidity = "";
  };
  const edited = (event: Event) => {
    const field = event.target;
    if (!isField(field) || !("koValidity" in field.dataset)) return;
    field.setCustomValidity("");
    delete field.dataset.koValidity;
  };
  document.addEventListener("invalid", invalid, true);
  document.addEventListener("input", edited, true);
  document.addEventListener("change", edited, true);
  return () => {
    document.removeEventListener("invalid", invalid, true);
    document.removeEventListener("input", edited, true);
    document.removeEventListener("change", edited, true);
  };
}
