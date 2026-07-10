import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { SearchPanel } from "./SearchPanel";
import { RecallHit, UserApi } from "./api";

afterEach(() => vi.restoreAllMocks());

it("renders semantic assertion recall payloads as relationships", async () => {
	const hit: RecallHit = {
		tier: "0.75",
		score: 0.96,
		semantic_rank: 0,
		keyword_rank: 0,
		final_score: 0.96,
		assertion: {
			assertion_id: "assertion-1",
			team_id: "team-1",
			subject_entity_id: "mark",
			predicate_key: "works_on",
			relationship_type: "WORKS_ON",
			object_entity_id: "dense-mem",
			tier: "fact",
			status: "active",
			policy_family: "versioned",
			polarity: "+",
			modality: "assertion",
			recorded_at: "2026-07-10T12:00:00Z",
			support_count: 2,
			source_group_count: 2,
		},
		paths: [{
			nodes: [
				{ key: "entity:mark", id: "mark", type: "person", name: "Mark" },
				{ key: "entity:dense-mem", id: "dense-mem", type: "project", name: "Dense-Mem" },
			],
			edges: [{
				assertion_id: "assertion-1",
				source: "entity:mark",
				target: "entity:dense-mem",
				relationship: "WORKS_ON",
				predicate: "works_on",
				tier: "fact",
				status: "active",
				polarity: "+",
			}],
		}],
	};
	vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: [hit] }), {
		status: 200,
		headers: { "Content-Type": "application/json" },
	}));

	render(<SearchPanel api={new UserApi("dm_read")} />);
	await userEvent.type(screen.getByLabelText("Keyword"), "dense-mem");
	await userEvent.click(screen.getByRole("button", { name: "Search" }));

	const resultList = await screen.findByRole("listbox", { name: "Recall result list" });
	expect(within(resultList).getByText("Mark")).toBeInTheDocument();
	expect(within(resultList).getByText("WORKS_ON: Dense-Mem")).toBeInTheDocument();
	expect(screen.getByLabelText("Inspector")).toHaveTextContent("Assertion");
	expect(screen.getByLabelText("Inspector")).toHaveTextContent("semantic graph");
});
