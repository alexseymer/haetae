/** The Haetae mark, same artwork as docs/public/logo.svg. */
export function LogoMark({ size = 32 }: { size?: number }) {
  return (
    <svg
      className="brand-mark"
      viewBox="0 0 32 32"
      width={size}
      height={size}
      aria-hidden="true"
      focusable="false"
    >
      <rect width="32" height="32" rx="7" fill="#142f66" />
      <g fill="#8fb0ff">
        <path d="M9 12h4v13H9z" />
        <path d="M19 12h4v13h-4z" />
        <path d="M13 17.5h6v3h-6z" />
        <path d="M9 12 6.2 5.5 13 9.2V12z" />
        <path d="M23 12 25.8 5.5 19 9.2V12z" />
      </g>
    </svg>
  );
}
