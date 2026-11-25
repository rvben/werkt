CREATE TABLE run_capacity_incidents (
    automation_id TEXT PRIMARY KEY REFERENCES automations(id) ON DELETE CASCADE
);
