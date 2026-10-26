#!/usr/bin/env python3
"""Compare Grype (haetae grypedb) findings vs Trivy JSON for ADR risk 3."""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

SEVERITY_RANK = {
    "critical": 4,
    "high": 3,
    "medium": 2,
    "low": 1,
    "negligible": 0,
    "unknown": -1,
    "unspecified": -1,
}


def slugify(image: str) -> str:
    # Match bench/m7/02-grype-trivy.sh: tr '/:' '__' (each of / and : -> _).
    return image.replace("/", "_").replace(":", "_")


def norm_sev(s: str) -> str:
    return (s or "unknown").strip().lower()


def cve_id(vuln_id: str) -> str:
    """Prefer CVE-… token when present (GHSA/ALAS often cross-ref)."""
    if not vuln_id:
        return ""
    m = re.search(r"(CVE-\d{4}-\d+)", vuln_id, re.I)
    if m:
        return m.group(1).upper()
    return vuln_id.strip()


def pair_key(vuln_id: str, package: str) -> tuple[str, str]:
    return (cve_id(vuln_id), (package or "").strip().lower())


def load_grype(path: Path) -> list[dict]:
    data = json.loads(path.read_text())
    return data.get("findings") or []


def load_trivy(path: Path) -> list[dict]:
    data = json.loads(path.read_text())
    out: list[dict] = []
    for result in data.get("Results") or []:
        for v in result.get("Vulnerabilities") or []:
            out.append(
                {
                    "vulnId": v.get("VulnerabilityID") or "",
                    "package": v.get("PkgName") or v.get("PkgID") or "",
                    "version": v.get("InstalledVersion") or "",
                    "severity": norm_sev(v.get("Severity") or ""),
                    "fixedVersion": v.get("FixedVersion") or "",
                    "title": v.get("Title") or "",
                    "status": v.get("Status") or "",
                }
            )
    return out


def filter_sev(findings: list[dict], min_rank: int | None) -> set[tuple[str, str]]:
    keys: set[tuple[str, str]] = set()
    for f in findings:
        sev = norm_sev(f.get("severity") or "")
        rank = SEVERITY_RANK.get(sev, -1)
        if min_rank is not None and rank < min_rank:
            continue
        k = pair_key(f.get("vulnId") or "", f.get("package") or "")
        if k[0] and k[1]:
            keys.add(k)
    return keys


def jaccard(a: set, b: set) -> float:
    if not a and not b:
        return 1.0
    u = a | b
    if not u:
        return 1.0
    return len(a & b) / len(u)


def index_findings(findings: list[dict]) -> dict[tuple[str, str], dict]:
    idx: dict[tuple[str, str], dict] = {}
    for f in findings:
        k = pair_key(f.get("vulnId") or "", f.get("package") or "")
        if k[0] and k[1]:
            idx[k] = f
    return idx


def guess_reason(key: tuple[str, str], only: str, g: dict | None, t: dict | None) -> str:
    gv = (g or {}).get("version") or ""
    tv = (t or {}).get("version") or ""
    gs = norm_sev((g or {}).get("severity") or "")
    ts = norm_sev((t or {}).get("severity") or "")
    gf = (g or {}).get("fixedVersion") or ""
    tf = (t or {}).get("fixedVersion") or ""
    status = (t or {}).get("status") or ""

    if only == "grype":
        if gf and not tf:
            return "Grype-only: likely distro-aware match / fixed-version present in Grype DB; Trivy omitted or different package naming"
        if gs in ("low", "negligible") or ts and SEVERITY_RANK.get(gs, -1) != SEVERITY_RANK.get(ts, -1):
            return "Grype-only at this threshold: severity / policy difference vs Trivy"
        return "Grype-only: advisory present in Anchore DB or package naming differs from Trivy"
    # only trivy
    if status and "will_not_fix" in status.lower():
        return "Trivy-only: unfixed / will_not_fix status policy differs from Grype"
    if "ubuntu" in ((t or {}).get("title") or "").lower() or "debian" in ((t or {}).get("title") or "").lower():
        return "Trivy-only: possible distro backport handling difference"
    if gf != tf and (gf or tf):
        return "Trivy-only: fixed-version / unfixed policy difference"
    if gs and ts and gs != ts:
        return f"severity differs (grype={gs or 'n/a'} trivy={ts}); may fall on other side of threshold"
    if gv and tv and gv != tv:
        return "package version string differs between scanners"
    return "Trivy-only: GHSA/OSV vs distro feed coverage or package name normalization"


def compare_image(image: str, grype_path: Path, trivy_path: Path) -> dict:
    gf = load_grype(grype_path)
    tf = load_trivy(trivy_path)
    g_idx = index_findings(gf)
    t_idx = index_findings(tf)

    bands = {
        "all": None,
        "ge_medium": 2,
    }
    band_out = {}
    for name, min_rank in bands.items():
        gs = filter_sev(gf, min_rank)
        ts = filter_sev(tf, min_rank)
        inter = gs & ts
        only_g = gs - ts
        only_t = ts - gs
        band_out[name] = {
            "grypeCount": len(gs),
            "trivyCount": len(ts),
            "intersection": len(inter),
            "onlyInGrype": len(only_g),
            "onlyInTrivy": len(only_t),
            "jaccard": round(jaccard(gs, ts), 4),
        }

        # Sample up to 10 disagreements for ge_medium (or all if empty).
        if name == "ge_medium" or (
            name == "all" and band_out.get("ge_medium", {}).get("onlyInGrype", 0)
            + band_out.get("ge_medium", {}).get("onlyInTrivy", 0)
            == 0
        ):
            samples = []
            for k in sorted(only_g)[:5]:
                samples.append(
                    {
                        "side": "only-in-grype",
                        "cve": k[0],
                        "package": k[1],
                        "grype": g_idx.get(k),
                        "trivy": t_idx.get(k),
                        "likelyReason": guess_reason(k, "grype", g_idx.get(k), t_idx.get(k)),
                    }
                )
            for k in sorted(only_t)[:5]:
                samples.append(
                    {
                        "side": "only-in-trivy",
                        "cve": k[0],
                        "package": k[1],
                        "grype": g_idx.get(k),
                        "trivy": t_idx.get(k),
                        "likelyReason": guess_reason(k, "trivy", g_idx.get(k), t_idx.get(k)),
                    }
                )
            # Prefer attaching samples on ge_medium; also keep for all if needed.
            band_out[name]["disagreementSamples"] = samples[:10]

    # Always produce 10 samples from ge_medium disagreements, falling back to all.
    samples = band_out["ge_medium"].get("disagreementSamples") or band_out["all"].get(
        "disagreementSamples"
    ) or []
    if len(samples) < 10:
        gs = filter_sev(gf, None)
        ts = filter_sev(tf, None)
        only_g = sorted(gs - ts)
        only_t = sorted(ts - gs)
        for k in only_g:
            if len(samples) >= 10:
                break
            if any(s["cve"] == k[0] and s["package"] == k[1] for s in samples):
                continue
            samples.append(
                {
                    "side": "only-in-grype",
                    "cve": k[0],
                    "package": k[1],
                    "grype": g_idx.get(k),
                    "trivy": None,
                    "likelyReason": guess_reason(k, "grype", g_idx.get(k), None),
                }
            )
        for k in only_t:
            if len(samples) >= 10:
                break
            if any(s["cve"] == k[0] and s["package"] == k[1] for s in samples):
                continue
            samples.append(
                {
                    "side": "only-in-trivy",
                    "cve": k[0],
                    "package": k[1],
                    "grype": None,
                    "trivy": t_idx.get(k),
                    "likelyReason": guess_reason(k, "trivy", None, t_idx.get(k)),
                }
            )

    return {
        "image": image,
        "grypeRawFindings": len(gf),
        "trivyRawFindings": len(tf),
        "bands": band_out,
        "disagreementSamples": samples[:10],
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--sbom-dir", type=Path, required=True)
    ap.add_argument("--grype-dir", type=Path, required=True)
    ap.add_argument("--trivy-dir", type=Path, required=True)
    ap.add_argument("--images", required=True, help="space-separated image refs")
    ap.add_argument("--out", type=Path, required=True)
    args = ap.parse_args()

    images = args.images.split()
    results = []
    for image in images:
        slug = slugify(image)
        gp = args.grype_dir / f"{slug}.json"
        tp = args.trivy_dir / f"{slug}.json"
        if not gp.exists() or not tp.exists():
            results.append({"image": image, "error": f"missing inputs gp={gp.exists()} tp={tp.exists()}"})
            continue
        results.append(compare_image(image, gp, tp))

    payload = {"images": results}
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(payload, indent=2) + "\n")
    print(json.dumps(payload, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
