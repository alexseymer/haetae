/**
 * Strip ANSI escape sequences and replace control characters other than
 * tab/newline. Does not interpret colours or styles.
 */
export function stripAnsi(input: string): string {
  // CSI / OSC / other ESC sequences (ECMA-48) and lone ESC.
  const withoutEscapes = input.replace(
    // eslint-disable-next-line no-control-regex -- intentional: strip C0/ESC
    /\u001b(?:\[[0-?]*[ -/]*[@-~]|\][^\u0007\u001b]*(?:\u0007|\u001b\\)|[@-Z\\-_])?/g,
    "",
  );
  // Keep tab (\t) and newline (\n); drop other C0 controls and DEL.
  // eslint-disable-next-line no-control-regex -- intentional: strip C0
  return withoutEscapes.replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, "");
}
