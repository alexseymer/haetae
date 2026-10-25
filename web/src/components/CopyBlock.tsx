import { useRef, useState } from "react";

interface CopyBlockProps {
  text: string;
  label?: string;
}

async function copyText(text: string, pre: HTMLPreElement | null): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through
  }
  if (!pre) return false;
  const range = document.createRange();
  range.selectNodeContents(pre);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    sel?.removeAllRanges();
  }
}

export function CopyBlock({ text, label = "Command" }: CopyBlockProps) {
  const preRef = useRef<HTMLPreElement>(null);
  const [copied, setCopied] = useState(false);

  const onCopy = async () => {
    const ok = await copyText(text, preRef.current);
    if (ok) {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    }
  };

  return (
    <div className="copy-block">
      <pre ref={preRef} className="mono" aria-label={label}>
        {text}
      </pre>
      <div className="copy-actions">
        <button type="button" className="btn btn-secondary" onClick={() => void onCopy()}>
          {copied ? "Copied" : "Copy"}
        </button>
        <span className="sr-only" aria-live="polite">
          {copied ? "Copied" : ""}
        </span>
      </div>
    </div>
  );
}
