// Heatsink fins: CHILL keeps inference cool by spending less energy.
export function BrandMark({className}:{className?:string}) {
  return <svg viewBox="0 0 24 24" className={className} aria-hidden="true" fill="currentColor">
    <rect x="2" y="17.5" width="20" height="3.5" rx="1.2" />
    <rect x="3.5" y="9" width="3" height="7.5" rx="1.5" />
    <rect x="8.25" y="4" width="3" height="12.5" rx="1.5" />
    <rect x="12.75" y="6.5" width="3" height="10" rx="1.5" />
    <rect x="17.5" y="11" width="3" height="5.5" rx="1.5" />
  </svg>
}

export function FinField({className}:{className?:string}) {
  const heights = [62, 118, 168, 204, 226, 214, 186, 146, 102, 70, 48, 34]
  return <svg viewBox="0 0 480 300" className={className} aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.5">
    {heights.map((height, index) => <rect key={index} x={18 + index * 38} y={262 - height} width="18" height={height} rx="9" opacity={0.35 + index % 3 * 0.2} />)}
    <rect x="6" y="268" width="468" height="22" rx="8" />
  </svg>
}
