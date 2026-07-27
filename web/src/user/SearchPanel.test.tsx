import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SearchPanel } from "./SearchPanel";
import { RecallHit, UserApi } from "./api";

describe("SearchPanel", () => {
  it("uses legacy relationship tier as a status fallback", async () => {
    const hits: RecallHit[] = [{
      tier: "candidate",
      relationship: {
        relationship_id: "relationship-legacy-candidate",
        tier: "candidate",
        subject: { name: "Dense-Mem" },
        predicate: "uses",
        object: { name: "PostgreSQL" },
        polarity: "+",
      },
      semantic_rank: 1,
      final_score: 1,
    }];
    const api = {
      recall: vi.fn().mockResolvedValue(hits),
    } as unknown as UserApi;

    render(<SearchPanel api={api} />);
    await userEvent.type(screen.getByLabelText("Keyword"), "postgres");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));

    const list = await screen.findByRole("listbox", { name: "Recall result list" });
    const option = within(list).getByRole("option");
    expect(within(option).getByText("Relationship")).toBeInTheDocument();
    expect(within(option).getByText("Provisional")).toBeInTheDocument();
    expect(option).toHaveTextContent("Dense-Mem");
    expect(option).toHaveTextContent("uses: PostgreSQL");
  });
});
