Vendored copy of github.com/blakesmith/ar (MIT, see COPYING) at 809d4375e1fb with one fix:
negative member sizes are treated as 0 (GO-2026-6905, https://github.com/blakesmith/ar/pull/10).
Used through a `replace` in go.mod. Drop it once upstream releases a fixed version.
