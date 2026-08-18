from __future__ import annotations

import json
import os
from pathlib import Path


event = json.loads(Path(os.environ["WERKT_EVENT_PATH"]).read_text())
print(json.dumps({"message": "Python received an event", "eventId": event["id"]}))

result = {
    "language": "python",
    "trigger": event["trigger"],
    "received": event["data"],
}
Path(os.environ["WERKT_RESULT_PATH"]).write_text(json.dumps(result))
