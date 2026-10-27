import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Project } from "../api/types";
import { ProjectForm } from "./ProjectForm";

const project: Project = {
  id: "p1",
  hostId: "h1",
  name: "shop",
  source: {
    kind: "git",
    gitUrl: "https://github.com/acme/shop",
    gitRef: "main",
    composePath: "compose.yaml",
  },
  domain: "shop.example.com",
  service: "web",
  port: 8080,
  autoDeploy: false,
  hasToken: true,
  createdAt: new Date().toISOString(),
};

describe("ProjectForm", () => {
  it("validates name, https URL, ref, and ingress together", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(
      <ProjectForm mode="create" onClose={() => {}} onSubmit={onSubmit} />,
    );
    expect(screen.getByRole("note")).toHaveTextContent(/root access/);

    await user.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText(/lowercase letters/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();

    await user.type(screen.getByLabelText("Name"), "Shop");
    await user.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText(/lowercase letters/i)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Name"));
    await user.type(screen.getByLabelText("Name"), "shop");
    await user.type(screen.getByLabelText("Repository URL"), "git@github.com:acme/x");
    await user.click(screen.getByRole("button", { name: "Create project" }));
    expect(await screen.findByText(/must start with https/i)).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Repository URL"));
    await user.type(
      screen.getByLabelText("Repository URL"),
      "https://github.com/acme/shop",
    );
    await user.clear(screen.getByLabelText("Ref"));
    await user.type(screen.getByLabelText("Ref"), "..bad");
    await user.click(screen.getByRole("button", { name: "Create project" }));
    expect(
      await screen.findByText(/branch name or a 40-character commit/i),
    ).toBeInTheDocument();

    await user.clear(screen.getByLabelText("Ref"));
    await user.type(screen.getByLabelText("Ref"), "main");
    await user.type(screen.getByLabelText("Domain"), "shop.example.com");
    await user.click(screen.getByRole("button", { name: "Create project" }));
    expect(
      await screen.findByText(/required together/i),
    ).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("keeps token write-only and omits it on edit unless changed", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(
      <ProjectForm
        mode="edit"
        project={project}
        onClose={() => {}}
        onSubmit={onSubmit}
      />,
    );
    expect(screen.queryByDisplayValue(/ghp_|token|secret/i)).not.toBeInTheDocument();
    expect(
      screen.getByText(/A token is stored. Leave empty to keep it/),
    ).toBeInTheDocument();
    const token = screen.getByLabelText("Access token");
    expect(token).toHaveAttribute("type", "password");
    expect(token).toHaveAttribute("autocomplete", "off");
    expect(token).toHaveValue("");

    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    const patch = onSubmit.mock.calls[0]![0].patch;
    expect(patch.source.gitToken).toBeUndefined();

    onSubmit.mockClear();
    await user.type(token, "new-token");
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    expect(onSubmit.mock.calls[0]![0].patch.source.gitToken).toBe("new-token");
  });

  it("clears token when Remove token is checked", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(
      <ProjectForm
        mode="edit"
        project={project}
        onClose={() => {}}
        onSubmit={onSubmit}
      />,
    );
    await user.click(screen.getByRole("checkbox", { name: /Remove token/i }));
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    expect(onSubmit.mock.calls[0]![0].patch.source.gitToken).toBe("");
  });
});
