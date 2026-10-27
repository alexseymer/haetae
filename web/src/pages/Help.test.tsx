import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { DOC_LINKS, GLOSSARY_ORDER, HELP } from "../lib/help";
import { HelpPage } from "./Help";

describe("HelpPage", () => {
  it("explains how Haetae works, defines the terms and links the docs", () => {
    render(
      <MemoryRouter>
        <HelpPage />
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { level: 1, name: "Help" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "How Haetae works" })).toBeInTheDocument();
    for (const t of [
      "Hosts and the agent",
      "Containers and metrics",
      "Incidents and alerts",
      "Chat",
      "AI operator (optional)",
      "Security scanning",
    ]) {
      expect(screen.getByRole("heading", { name: t })).toBeInTheDocument();
    }

    const glossary = screen.getByRole("heading", { name: "Glossary" }).closest("section")!;
    for (const term of GLOSSARY_ORDER) {
      expect(within(glossary).getByText(HELP[term].title)).toBeInTheDocument();
    }
    for (const required of ["Severity", "Autonomy", "Incident", "Alert", "Matching pending", "Scan failed", "Rollback"]) {
      expect(within(glossary).getByText(required)).toBeInTheDocument();
    }

    for (const d of DOC_LINKS) {
      const link = screen.getByRole("link", { name: d.label });
      expect(link).toHaveAttribute(
        "href",
        `https://github.com/alexseymer/haetae/blob/main/docs/${d.doc}.md`,
      );
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
    }
    expect(DOC_LINKS.map((d) => d.doc).sort()).toEqual(
      ["ai-operator", "chat", "deploy", "enroll", "install", "security", "troubleshooting"],
    );
  });
});
