package authoring

import (
	"encoding/json"
	"fmt"
	"strings"
)

func renderFiles(draft Draft, selected []selectedOperation, requiresApproval bool) []File {
	return []File{
		{Path: "main.py", Content: renderMain(selected, requiresApproval)},
		{Path: "workflow_plan.py", Content: renderPlanModule(draft)},
		{Path: "test_main.py", Content: renderMainTest(requiresApproval)},
		{Path: "test_workflow_plan.py", Content: renderPlanTest(requiresApproval)},
	}
}

func renderMainTest(requiresApproval bool) string {
	approvalTest := ""
	if requiresApproval {
		approvalTest = `
    def test_approval_request_and_rejection_do_not_execute_operations(self):
        with tempfile.TemporaryDirectory() as directory:
            context = Context("automation", "revision", "run", Path(directory) / "control.json", state={})
            original = {"spreadsheet_id": "sheet", "range": "A1:B2", "model": "model"}
            event = Event("event", "", "", {"type": "schedule"}, original, {})
            self.assertEqual(handle(event, context), {"outcome": "approval-requested"})
            self.assertEqual(context.state["draftInput"], original)
            self.assertEqual(context.control.approval["actions"][0]["id"], "approve")

            rejection = Event("event-approval", "", "", {"type": "approval"}, {
                "approval": {"approvalId": "approval", "action": "reject", "fields": {}},
            }, {})
            self.assertEqual(handle(rejection, context), {"outcome": "rejected"})
            self.assertNotIn("draftInput", context.state)
`
	}
	return fmt.Sprintf(`import tempfile
import unittest
from pathlib import Path

try:
    from main import _required, _text, handle
    from werkt import Context, Event
except ModuleNotFoundError:
    Context = Event = handle = None


@unittest.skipIf(handle is None, "Werkt SDK is injected by Werkt deployment checks")
class GeneratedMainTest(unittest.TestCase):
    def test_input_helpers_are_strict_and_deterministic(self):
        self.assertEqual(_required({"value": 7}, "value"), 7)
        with self.assertRaises(ValueError):
            _required({}, "value")
        self.assertEqual(_text({"b": 2, "a": 1}), '{"a":1,"b":2}')
%s

if __name__ == "__main__":
    unittest.main()
`, approvalTest)
}

func renderPlanModule(draft Draft) string {
	steps, _ := json.Marshal(draft.Steps)
	decision, _ := json.Marshal(draft.Decision)
	return fmt.Sprintf(`"""Deterministic workflow plan compiled from typed Jev decisions."""

import json


STEPS = json.loads(r'''%s''')
DECISION = json.loads(r'''%s''')


def validate_plan():
    seen = set()
    for step in STEPS:
        missing = set(step["depends_on"]) - seen
        if missing:
            raise ValueError(f"{step['id']} has unordered dependencies: {sorted(missing)}")
        if step["id"] in seen:
            raise ValueError(f"duplicate step: {step['id']}")
        seen.add(step["id"])
    return True
`, steps, decision)
}

func renderPlanTest(requiresApproval bool) string {
	approvalAssertion := "self.assertNotIn('review', by_id)"
	if requiresApproval {
		approvalAssertion = `self.assertIn("review", by_id)
        for step in STEPS:
            if step["effect"] == "write":
                self.assertIn("review", step["depends_on"])`
	}
	return fmt.Sprintf(`import unittest

from workflow_plan import DECISION, STEPS, validate_plan


class WorkflowPlanTest(unittest.TestCase):
    def test_graph_is_topologically_ordered(self):
        self.assertTrue(validate_plan())

    def test_jev_decisions_are_auditable(self):
        self.assertEqual(DECISION["provider"], "typesafe")
        self.assertTrue(DECISION["model"].startswith("jev-"))
        self.assertGreaterEqual(DECISION["confidence"], 0)
        self.assertLessEqual(DECISION["confidence"], 1)

    def test_approval_policy(self):
        by_id = {step["id"]: step for step in STEPS}
        %s


if __name__ == "__main__":
    unittest.main()
`, approvalAssertion)
}

func renderMain(selected []selectedOperation, requiresApproval bool) string {
	imports := map[string]bool{}
	for _, operation := range selected {
		switch operation.Spec.Connector {
		case "google-sheets":
			imports["GoogleServiceAccount"] = true
			imports["GoogleSheets"] = true
		case "openai":
			imports["OpenAI"] = true
		case "zoom":
			imports["Zoom"] = true
		}
	}
	names := []string{}
	for _, name := range []string{"GoogleServiceAccount", "GoogleSheets", "OpenAI", "Zoom"} {
		if imports[name] {
			names = append(names, name)
		}
	}
	var value strings.Builder
	value.WriteString("from __future__ import annotations\n\nimport json\nimport os\n")
	if requiresApproval {
		value.WriteString("from datetime import UTC, datetime, timedelta\n")
	}
	value.WriteString("from pathlib import Path\n\nfrom werkt import Context, Event, automation, execute\n")
	if len(names) > 0 {
		fmt.Fprintf(&value, "from werkt.connectors import %s\n", strings.Join(names, ", "))
	}
	value.WriteString(`

def _required(data: dict, key: str):
    value = data.get(key)
    if value is None or value == "":
        raise ValueError(f"event.data.{key} is required")
    return value


def _text(value) -> str:
    return value if isinstance(value, str) else json.dumps(value, separators=(",", ":"), sort_keys=True)


@automation
def handle(event: Event, context: Context) -> dict:
    if not isinstance(event.data, dict):
        raise ValueError("event.data must be an object")
    data = event.data
`)
	if requiresApproval {
		value.WriteString(`    approval = data.get("approval")
    if not isinstance(approval, dict) or "approvalId" not in approval:
        context.state["draftInput"] = data
        context.request_approval(
            key=f"{event.id}.review",
            title="Review generated workflow execution",
            description="Confirm the input and side effects before this draft performs its work.",
            expires_at=datetime.now(UTC) + timedelta(days=7),
            fields=[],
            actions=[
                {"id": "approve", "label": "Run workflow", "style": "primary"},
                {"id": "reject", "label": "Cancel", "style": "neutral"},
            ],
        )
        return {"outcome": "approval-requested"}
    if approval.get("action") != "approve":
        context.state.pop("draftInput", None)
        return {"outcome": "rejected"}
    data = context.state.pop("draftInput", None)
    if not isinstance(data, dict):
        raise ValueError("approved execution is missing its original input")
`)
	}
	value.WriteString("    current = data.get(\"input\", data)\n    results = {}\n")
	for _, operation := range selected {
		renderOperation(&value, operation.Spec.ID)
	}
	value.WriteString("    return {\"outcome\": \"completed\", \"result\": current, \"steps\": results}\n\n\nif __name__ == \"__main__\":\n    execute(handle)\n")
	return value.String()
}

func renderOperation(value *strings.Builder, id string) {
	switch id {
	case "zoom-recording":
		value.WriteString(`    zoom = Zoom(os.environ["ZOOM_ACCOUNT_ID"], os.environ["ZOOM_CLIENT_ID"], os.environ["ZOOM_CLIENT_SECRET"], allowed_download_hosts=set())
    current = zoom.recording(str(_required(data, "recording_uuid")))
`)
	case "sheets-read":
		value.WriteString(`    sheets = GoogleSheets(GoogleServiceAccount.from_json(os.environ["GOOGLE_SERVICE_ACCOUNT_JSON"]))
    current = sheets.values(str(_required(data, "spreadsheet_id")), str(_required(data, "range")))
`)
	case "openai-transcribe":
		value.WriteString(`    ai = OpenAI(os.environ["OPENAI_API_KEY"])
    current = ai.transcribe(Path(str(_required(data, "audio_path"))), model=str(data.get("model", "whisper-1")))
`)
	case "openai-vision":
		value.WriteString(`    ai = OpenAI(os.environ["OPENAI_API_KEY"])
    current = ai.vision(model=str(_required(data, "model")), prompt=str(_required(data, "prompt")), image=Path(str(_required(data, "image_path"))))
`)
	case "openai-chat":
		value.WriteString(`    ai = OpenAI(os.environ["OPENAI_API_KEY"])
    prompt = str(data.get("prompt", "Process the supplied content accurately and concisely."))
    current = ai.chat(model=str(_required(data, "model")), messages=[{"role": "user", "content": prompt + "\n\n" + _text(current)}], temperature=0)
`)
	case "operator-notify":
		value.WriteString(`    context.notify(key=f"{event.id}.result", title=str(data.get("notification_title", "Automation completed")), body=_text(current)[:4000])
`)
	case "sheets-update":
		value.WriteString(`    sheets = GoogleSheets(GoogleServiceAccount.from_json(os.environ["GOOGLE_SERVICE_ACCOUNT_JSON"]))
    rows = current if isinstance(current, list) and all(isinstance(row, list) for row in current) else [[_text(current)]]
    sheets.update(str(_required(data, "spreadsheet_id")), str(_required(data, "range")), rows)
`)
	case "sheets-clear":
		value.WriteString(`    sheets = GoogleSheets(GoogleServiceAccount.from_json(os.environ["GOOGLE_SERVICE_ACCOUNT_JSON"]))
    sheets.clear(str(_required(data, "spreadsheet_id")), str(_required(data, "range")))
    current = {"cleared": True}
`)
	}
	fmt.Fprintf(value, "    results[%q] = current\n", id)
}
