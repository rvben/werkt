-- Private deployment extension for the pinned Werkt runs/investigations schema.
-- Requests commit atomically with successful classifier results. NOTIFY is only
-- a wakeup: the table, not a notification payload, is the durable work source.
BEGIN;
DO $$ BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'werkt-investigator') THEN
    CREATE ROLE "werkt-investigator" LOGIN;
  END IF;
END $$;
CREATE SCHEMA IF NOT EXISTS investigation_dispatch;
REVOKE ALL ON SCHEMA investigation_dispatch FROM PUBLIC;
CREATE TABLE IF NOT EXISTS investigation_dispatch.requests (
  repository TEXT NOT NULL,
  issue_number BIGINT NOT NULL CHECK (issue_number > 0),
  generation BIGSERIAL NOT NULL,
  source_run TEXT NOT NULL,
  pending BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (repository, issue_number)
);
CREATE INDEX IF NOT EXISTS investigation_dispatch_pending
  ON investigation_dispatch.requests (updated_at) WHERE pending;
GRANT USAGE ON SCHEMA investigation_dispatch TO "werkt-investigator";
GRANT SELECT, UPDATE (pending) ON investigation_dispatch.requests TO "werkt-investigator";

CREATE OR REPLACE FUNCTION investigation_dispatch.collect_requests()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, investigation_dispatch AS $$
DECLARE item jsonb; queued boolean := false;
BEGIN
  IF NEW.automation_id <> 'github-issue-labeler' OR NEW.status <> 'succeeded'
     OR OLD.status = 'succeeded' OR NEW.result->>'mutationMode' IS DISTINCT FROM 'production'
     OR jsonb_typeof(NEW.result->'investigationRequests') IS DISTINCT FROM 'array' THEN
    RETURN NEW;
  END IF;
  FOR item IN SELECT value FROM jsonb_array_elements(NEW.result->'investigationRequests') LOOP
    IF item->>'urgency' = 'high'
       AND item->>'repository' ~ '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
       AND item->>'issueNumber' ~ '^[1-9][0-9]{0,17}$' THEN
      INSERT INTO investigation_dispatch.requests(repository, issue_number, source_run)
      VALUES (item->>'repository', (item->>'issueNumber')::bigint, NEW.id)
      ON CONFLICT (repository, issue_number) DO UPDATE
      SET generation = EXCLUDED.generation, source_run = EXCLUDED.source_run,
          pending = true, updated_at = now();
      queued := true;
    END IF;
  END LOOP;
  IF queued THEN
    PERFORM pg_notify('werkt_investigation_dispatch', '');
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION investigation_dispatch.collect_requests() FROM PUBLIC;
DROP TRIGGER IF EXISTS investigation_dispatch_requests ON public.runs;
CREATE TRIGGER investigation_dispatch_requests AFTER UPDATE OF status ON public.runs
FOR EACH ROW EXECUTE FUNCTION investigation_dispatch.collect_requests();

CREATE OR REPLACE FUNCTION investigation_dispatch.wake_on_review_completion()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
  IF NEW.state->>'status' IN ('completed', 'abandoned', 'review', 'local')
     AND OLD.state->>'status' IS DISTINCT FROM NEW.state->>'status' THEN
    PERFORM pg_notify('werkt_investigation_dispatch', '');
  END IF;
  RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION investigation_dispatch.wake_on_review_completion() FROM PUBLIC;
DROP TRIGGER IF EXISTS investigation_dispatch_reviewed ON public.investigations;
CREATE TRIGGER investigation_dispatch_reviewed AFTER UPDATE OF state ON public.investigations
FOR EACH ROW EXECUTE FUNCTION investigation_dispatch.wake_on_review_completion();
COMMIT;
